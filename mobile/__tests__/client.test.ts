import {ApiClient, normalizeServerUrl, TinyPasswordApi} from '../src/api/client';
import type {FetchInit, FetchResponseLike, FetchLike} from '../src/api/client';

function setFetch(client: ApiClient, fn: FetchLike): void {
  (client as unknown as {fetchImpl: FetchLike}).fetchImpl = fn;
}

function jsonResponse(
  status: number,
  body: unknown,
  setCookie: string[] = [],
  extraHeaders: Record<string, string> = {},
): FetchResponseLike {
  return {
    status,
    headers: {
      get: (name: string) => extraHeaders[name.toLowerCase()] ?? null,
      getSetCookie: () => setCookie,
    },
    text: async () => (body === undefined ? '' : JSON.stringify(body)),
  };
}

describe('normalizeServerUrl', () => {
  it('trims paths and trailing slashes', () => {
    expect(normalizeServerUrl('  https://vault.example.com/some/path/  ')).toBe(
      'https://vault.example.com',
    );
    expect(normalizeServerUrl('http://10.0.2.2:8080')).toBe('http://10.0.2.2:8080');
  });

  it('rejects garbage', () => {
    expect(normalizeServerUrl('')).toBeNull();
    expect(normalizeServerUrl('vault.example.com')).toBeNull();
    expect(normalizeServerUrl('ftp://x')).toBeNull();
  });
});

describe('ApiClient request plumbing', () => {
  it('sends matching cookies and records Set-Cookie from responses', async () => {
    const client = new ApiClient('https://vault.example.com');
    const seen: FetchInit[] = [];
    setFetch(client, async (url: string, init: FetchInit) => {
      seen.push(init);
      return jsonResponse(200, {csrf_token: 't1'}, ['tiny_password_preauth=ctx1; Path=/; HttpOnly']);
    });
    const first = await client.request<{csrf_token: string}>({method: 'POST', path: '/csrf'});
    expect(first.kind).toBe('success');
    expect(seen[0].headers.Cookie).toBeUndefined(); // nothing stored yet
    expect(client.jar.has('tiny_password_preauth')).toBe(true);

    await client.request({method: 'POST', path: '/csrf'});
    expect(seen[1].headers.Cookie).toBe('tiny_password_preauth=ctx1');
  });

  it('parses the error envelope into a stable ApiError', async () => {
    const client = new ApiClient('https://vault.example.com');
    setFetch(client, async () =>
      jsonResponse(409, {
        code: 'REVISION_CONFLICT',
        message: 'the item was updated by someone else',
        request_id: 'req-1',
        current_revision: 4,
      }),
    );
    const result = await client.request({method: 'PUT', path: '/items/x', body: {}});
    expect(result.kind).toBe('http-error');
    if (result.kind === 'http-error') {
      expect(result.status).toBe(409);
      expect(result.error.code).toBe('REVISION_CONFLICT');
      expect(result.error.currentRevision).toBe(4);
    }
  });

  it('treats 204 as an empty success', async () => {
    const client = new ApiClient('https://vault.example.com');
    setFetch(client, async () => jsonResponse(204, undefined));
    const result = await client.request<void>({method: 'POST', path: '/auth/logout'});
    if (result.kind !== 'success') {
      throw new Error('expected success');
    }
    expect(result.data).toBeUndefined();
  });

  it('reports transport failures and aborts distinctly', async () => {
    const client = new ApiClient('https://vault.example.com');
    setFetch(client, async () => {
      throw new Error('ECONNREFUSED');
    });
    const network = await client.request({method: 'GET', path: '/items'});
    expect(network.kind).toBe('network-error');

    const aborting = new AbortController();
    setFetch(
      client,
      (_url, init) =>
        new Promise((_, reject) => {
          init.signal?.addEventListener('abort', () => reject(new Error('AbortError')));
        }),
    );
    const pending = client.request({method: 'GET', path: '/items', signal: aborting.signal});
    aborting.abort();
    expect((await pending).kind).toBe('aborted');
  });

  it('times out requests as a network error (never a silent abort)', async () => {
    const client = new ApiClient('https://vault.example.com');
    setFetch(
      client,
      (_url, init) =>
        new Promise<FetchResponseLike>((_, reject) => {
          init.signal?.addEventListener('abort', () => reject(new Error('AbortError')));
        }),
    );
    const result = await client.request({method: 'GET', path: '/items', timeoutMs: 20});
    expect(result.kind).toBe('network-error');
    if (result.kind === 'network-error') {
      expect(result.message).toContain('超时');
    }
  });

  it('keeps user-initiated aborts distinct from timeouts', async () => {
    const client = new ApiClient('https://vault.example.com');
    setFetch(
      client,
      (_url, init) =>
        new Promise<FetchResponseLike>((_, reject) => {
          init.signal?.addEventListener('abort', () => reject(new Error('AbortError')));
        }),
    );
    const controller = new AbortController();
    const pending = client.request({
      method: 'GET',
      path: '/items',
      signal: controller.signal,
      timeoutMs: 60000,
    });
    controller.abort();
    expect((await pending).kind).toBe('aborted');
  });

  it('surfaces Retry-After from rate-limited responses', async () => {
    const client = new ApiClient('https://vault.example.com');
    setFetch(
      client,
      async () =>
        jsonResponse(
          429,
          {code: 'RATE_LIMITED', message: 'too many attempts; slow down'},
          [],
          {'retry-after': '60'},
        ),
    );
    const result = await client.request({method: 'POST', path: '/auth/login', body: {}});
    expect(result.kind).toBe('http-error');
    if (result.kind === 'http-error') {
      expect(result.error.retryAfterSeconds).toBe(60);
    }
  });
});

describe('TinyPasswordApi endpoint shapes', () => {
  it('list/search are pinned to personal login scope', async () => {
    const client = new ApiClient('https://vault.example.com');
    const urls: string[] = [];
    const bodies: unknown[] = [];
    setFetch(client, async (url: string, init: FetchInit) => {
      urls.push(url);
      bodies.push(init.body ? JSON.parse(init.body) : undefined);
      return jsonResponse(200, {items: [], next_cursor: null});
    });
    const api = new TinyPasswordApi(client);
    await api.listItems('cursor-1');
    expect(urls[0]).toContain('/items?scope=personal&type=login');
    expect(urls[0]).toContain('cursor=cursor-1');

    await api.searchItems('git hub', 'cursor-2', 'csrf-1');
    expect(urls[1]).toContain('/items/search');
    expect(bodies[1]).toMatchObject({query: 'git hub', scope: 'personal', type: 'login', cursor: 'cursor-2'});
  });

  it('update omits tags/favorite/vault_scope and sends the revision', async () => {
    const client = new ApiClient('https://vault.example.com');
    let body: any;
    setFetch(client, async (_url: string, init: FetchInit) => {
      body = JSON.parse(init.body!);
      return jsonResponse(200, {id: 'x', revision: 2, payload: {}});
    });
    const api = new TinyPasswordApi(client);
    await api.updateItem('x', 1, {name: 'n', username: '', password: ''}, 'csrf-1');
    expect(body).toEqual({revision: 1, payload: {name: 'n', username: '', password: ''}});
    expect(Object.keys(body).sort()).toEqual(['payload', 'revision']);
  });

  it('create carries the Idempotency-Key header', async () => {
    const client = new ApiClient('https://vault.example.com');
    const headers: Record<string, string>[] = [];
    setFetch(client, async (_url: string, init: FetchInit) => {
      headers.push(init.headers);
      return jsonResponse(201, {id: 'x', revision: 1, payload: {}});
    });
    const api = new TinyPasswordApi(client);
    await api.createItem({name: 'n', username: '', password: ''}, 'idem-key-123456', 'csrf-1');
    expect(headers[0]['Idempotency-Key']).toBe('idem-key-123456');
    expect(headers[0]['X-CSRF-Token']).toBe('csrf-1');
  });
});
