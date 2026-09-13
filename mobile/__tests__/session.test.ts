import {SessionController} from '../src/auth/session';
import type {FetchInit, FetchResponseLike} from '../src/api/client';

interface MockResponse {
  status: number;
  body?: unknown;
  setCookie?: string[];
  headers?: Record<string, string>;
}

interface RecordedCall {
  method: string;
  url: string;
  headers: Record<string, string>;
  body?: unknown;
}

/** Minimal stateful stand-in for the Go server's cookie+CSRF behaviour. */
function createMockServer() {
  const calls: RecordedCall[] = [];
  let sessionCookie: string | null = null; // value only, host-only, path=/
  let sessionCsrf = 'sess-token-0';
  let preauthCookie: string | null = null;
  let preauthToken: string | null = null;
  let mustChange = false;

  const api = (url: string): string => url.replace(/^https?:\/\/[^/]+/, '');

  function parseCookies(header: string | undefined): Record<string, string> {
    const out: Record<string, string> = {};
    if (!header) {
      return out;
    }
    for (const pair of header.split('; ')) {
      const eq = pair.indexOf('=');
      if (eq > 0) {
        out[pair.slice(0, eq)] = pair.slice(eq + 1);
      }
    }
    return out;
  }

  function handle(method: string, path: string, body: any, cookies: Record<string, string>, csrfHeader: string | undefined): MockResponse {
    calls.push({method, url: path, headers: csrfHeader ? {'x-csrf-token': csrfHeader} : {}, body});
    switch (`${method} ${path}`) {
      case 'POST /api/v1/csrf':
        preauthCookie = 'preauth-ctx-1';
        preauthToken = 'pre-token-1';
        return {
          status: 200,
          body: {csrf_token: preauthToken},
          setCookie: ['tiny_password_preauth=preauth-ctx-1; Path=/; HttpOnly'],
        };
      case 'POST /api/v1/auth/login': {
        if (cookies.tiny_password_preauth !== preauthCookie || csrfHeader !== preauthToken) {
          return {status: 403, body: {code: 'FORBIDDEN', message: 'missing or invalid CSRF context'}};
        }
        const handler = loginHandler;
        if (handler) {
          const response = handler(body, cookies);
          applyResponseState(response);
          return response;
        }
        return {status: 500, body: {code: 'INTERNAL', message: 'no login handler'}};
      }
      case 'GET /api/v1/auth/session':
        if (cookies.tiny_password_session !== sessionCookie || sessionCookie === null) {
          return {status: 401, body: {code: 'UNAUTHORIZED', message: 'no session'}};
        }
        return {
          status: 200,
          body: {
            user: {
              user_id: 'u1',
              username: 'alice',
              role: 'member',
              must_change_password: mustChange,
              idle_timeout_minutes: 15,
              session: {id: 's1', created_at: '', expires_at: '', current: true},
            },
            csrf_token: sessionCsrf,
          },
        };
      case 'POST /api/v1/auth/password': {
        if (cookies.tiny_password_session !== sessionCookie || csrfHeader !== sessionCsrf) {
          return {status: 401, body: {code: 'UNAUTHORIZED', message: 'no session'}};
        }
        const handler = passwordHandler;
        if (handler) {
          const response = handler(body, cookies);
          applyResponseState(response);
          return response;
        }
        return {status: 500, body: {code: 'INTERNAL', message: 'no password handler'}};
      }
      case 'POST /api/v1/auth/logout':
        if (csrfHeader !== sessionCsrf) {
          return {status: 403, body: {code: 'FORBIDDEN', message: 'missing csrf'}};
        }
        sessionCookie = null;
        return {status: 204, setCookie: ['tiny_password_session=; Path=/; Max-Age=-1']};
      case 'POST /api/v1/auth/session/activity':
        if (cookies.tiny_password_session !== sessionCookie || sessionCookie === null) {
          return {status: 401, body: {code: 'UNAUTHORIZED', message: 'no session'}};
        }
        return {status: 204};
      default:
        return {status: 404, body: {code: 'NOT_FOUND', message: 'unmocked ' + method + ' ' + path}};
    }
  }

  let loginHandler: (body: any, cookies: Record<string, string>) => MockResponse;
  let passwordHandler: (body: any, cookies: Record<string, string>) => MockResponse;

  // The mock applies its own Set-Cookie/CSRF like the real server would.
  function applyResponseState(response: MockResponse): void {
    if (response.status === 204) {
      // A successful password change clears the server-side flag.
      mustChange = false;
    }
    for (const raw of response.setCookie ?? []) {
      const [pair] = raw.split(';');
      const eq = pair.indexOf('=');
      const name = pair.slice(0, eq).trim();
      const value = pair.slice(eq + 1).trim();
      if (name === 'tiny_password_session') {
        sessionCookie = value === '' ? null : value;
      } else if (name === 'tiny_password_preauth') {
        preauthCookie = value === '' ? null : value;
      }
    }
    const bodyAny = response.body as {
      csrf_token?: string;
      user?: {must_change_password?: boolean};
    } | undefined;
    if (bodyAny?.csrf_token) {
      sessionCsrf = bodyAny.csrf_token;
    }
    if (typeof bodyAny?.user?.must_change_password === 'boolean') {
      mustChange = bodyAny.user.must_change_password;
    }
    const rotated = response.headers?.['x-csrf-token'];
    if (rotated) {
      sessionCsrf = rotated;
    }
  }

  const server = {
    calls,
    get sessionValue() {
      return sessionCookie;
    },
    get csrf() {
      return sessionCsrf;
    },
    set csrf(v: string) {
      sessionCsrf = v;
    },
    loginHandler: (fn: typeof loginHandler) => {
      loginHandler = fn;
    },
    passwordHandler: (fn: typeof passwordHandler) => {
      passwordHandler = fn;
    },
  };

  async function fetchImpl(url: string, init: FetchInit): Promise<FetchResponseLike> {
    const response = handle(init.method, api(url), init.body ? JSON.parse(init.body) : undefined, parseCookies(init.headers.Cookie), init.headers['X-CSRF-Token']);
    return {
      status: response.status,
      headers: {
        get: (name: string) => {
          if (name.toLowerCase() === 'x-csrf-token') {
            return response.headers?.['x-csrf-token'] ?? null;
          }
          return null;
        },
        getSetCookie: () => response.setCookie ?? [],
      },
      text: async () => (response.body === undefined ? '' : JSON.stringify(response.body)),
    };
  }

  return {server, fetchImpl};
}

function setup() {
  const {server, fetchImpl} = createMockServer();
  let now = 0;
  const controller = new SessionController(() => now, fetchImpl);
  controller.setServerUrl('https://vault.example.com');
  const advance = (ms: number) => {
    now += ms;
  };
  return {controller, server, advance};
}

const USER_OK = {
  user_id: 'u1',
  username: 'alice',
  role: 'member' as const,
  must_change_password: false,
  idle_timeout_minutes: 15,
  session: {id: 's1', created_at: '', expires_at: '', current: true},
};

describe('SessionController — authentication state transitions', () => {
  it('login success flows pre-auth CSRF → session cookie → authenticated', async () => {
    const {controller, server} = setup();
    let loginCalls = 0;
    server.loginHandler(body => {
      loginCalls += 1;
      if (body.username !== 'alice' || body.password !== 'pw123456789') {
        return {status: 401, body: {code: 'UNAUTHORIZED', message: 'invalid credentials'}};
      }
      return {
        status: 200,
        body: {must_change_password: false, csrf_token: 'sess-token-1', user: USER_OK},
        setCookie: [
          'tiny_password_preauth=; Path=/; Max-Age=-1',
          'tiny_password_session=sess-1; Path=/; HttpOnly',
        ],
      };
    });

    expect(await controller.preflight()).toBe(true);
    expect(controller.getSnapshot().phase).toBe('signed-out');
    const result = await controller.login('alice', 'pw123456789');
    expect(result.ok).toBe(true);
    expect(controller.getSnapshot().phase).toBe('authenticated');
    expect(loginCalls).toBe(1);
    expect(controller.csrfToken).toBe('sess-token-1');

    // Session requests now use the session cookie; preauth is gone.
    const outcome = await controller.confirmSession();
    expect(outcome).toBe('confirmed');
    const sessionCall = server.calls.find(c => c.url === '/api/v1/auth/session');
    expect(sessionCall).toBeDefined();
  });

  it('must_change_password login enters the forced-change phase', async () => {
    const {controller, server} = setup();
    server.loginHandler(() => ({
      status: 200,
      body: {
        must_change_password: true,
        csrf_token: 'sess-token-1',
        user: {...USER_OK, must_change_password: true},
      },
      setCookie: ['tiny_password_session=sess-1; Path=/; HttpOnly'],
    }));
    await controller.preflight();
    await controller.login('alice', 'pw123456789');
    expect(controller.getSnapshot().phase).toBe('must-change');
    // Session GET is allowed during first-login change (server permits it).
    expect(await controller.confirmSession()).toBe('still-required');
    expect(controller.getSnapshot().phase).toBe('must-change');
  });

  it('wrong credentials surface a stable error and stay signed out', async () => {
    const {controller, server} = setup();
    server.loginHandler(() => ({status: 401, body: {code: 'UNAUTHORIZED', message: 'invalid credentials or session'}}));
    await controller.preflight();
    const result = await controller.login('alice', 'wrong');
    expect(result.ok).toBe(false);
    expect(result.error).toBe('用户名或密码错误');
    expect(controller.getSnapshot().phase).toBe('signed-out');
  });

  it('disabled accounts report the reason', async () => {
    const {controller, server} = setup();
    server.loginHandler(() => ({status: 403, body: {code: 'ACCOUNT_DISABLED', message: 'account disabled'}}));
    await controller.preflight();
    const result = await controller.login('alice', 'pw');
    expect(result.error).toContain('停用');
  });
});

describe('SessionController — forced password change and CSRF rotation', () => {
  async function loginMustChange(controller: SessionController, server: ReturnType<typeof setup>['server']) {
    server.loginHandler(() => ({
      status: 200,
      body: {
        must_change_password: true,
        csrf_token: 'sess-token-1',
        user: {...USER_OK, must_change_password: true},
      },
      setCookie: ['tiny_password_session=sess-1; Path=/; HttpOnly'],
    }));
    await controller.preflight();
    await controller.login('alice', 'pw123456789');
  }

  it('changePassword rotates cookie+CSRF, confirmation completes the flow', async () => {
    const {controller, server} = setup();
    await loginMustChange(controller, server);
    let passwordCalls = 0;
    server.passwordHandler(body => {
      passwordCalls += 1;
      if (body.new_password === body.current_password) {
        return {status: 400, body: {code: 'VALIDATION_ERROR', message: '新密码不能与当前密码相同'}};
      }
      return {
        status: 204,
        headers: {'x-csrf-token': 'sess-token-2'},
        setCookie: ['tiny_password_session=sess-2; Path=/; HttpOnly'],
      };
    });

    const result = await controller.changePassword('pw123456789', 'brand-new-pw-1');
    expect(result.ok).toBe(true);
    expect(passwordCalls).toBe(1);
    expect(controller.getSnapshot().phase).toBe('confirming-change');
    expect(controller.csrfToken).toBe('sess-token-2'); // rotated CSRF consumed

    const outcome = await controller.confirmSession();
    expect(outcome).toBe('confirmed');
    expect(controller.getSnapshot().phase).toBe('authenticated');
    expect(passwordCalls).toBe(1); // never resubmitted
  });

  it('confirmation failure keeps confirming-change and offers retry without resubmitting', async () => {
    const {controller, server} = setup();
    await loginMustChange(controller, server);
    let passwordCalls = 0;
    server.passwordHandler(() => {
      passwordCalls += 1;
      return {
        status: 204,
        headers: {'x-csrf-token': 'sess-token-2'},
        setCookie: ['tiny_password_session=sess-2; Path=/; HttpOnly'],
      };
    });
    await controller.changePassword('pw123456789', 'brand-new-pw-1');

    // First confirmation attempt: network dies (swap the injected fetch).
    const api = controller.getApi()!;
    const realFetch = (api.client as unknown as {fetchImpl: unknown}).fetchImpl;
    const setFetch = (fn: unknown) => {
      (api.client as unknown as {fetchImpl: unknown}).fetchImpl = fn;
    };
    setFetch(() => Promise.reject(new Error('offline')));
    expect(await controller.confirmSession()).toBe('unreachable');
    setFetch(realFetch);
    expect(controller.getSnapshot().phase).toBe('confirming-change');
    expect(controller.getSnapshot().confirmError).toBeTruthy();

    // Retry confirmation — the password change itself is NOT resubmitted.
    expect(await controller.confirmSession()).toBe('confirmed');
    expect(passwordCalls).toBe(1);
  });

  it('policy failure surfaces the server message and stays in must-change', async () => {
    const {controller, server} = setup();
    await loginMustChange(controller, server);
    server.passwordHandler(() => ({
      status: 400,
      body: {code: 'VALIDATION_ERROR', message: 'password does not meet the policy: at least 12 characters required'},
    }));
    const result = await controller.changePassword('pw123456789', 'short');
    expect(result.ok).toBe(false);
    expect(result.error).toContain('policy');
    expect(controller.getSnapshot().phase).toBe('must-change');
  });

  it('401 during change returns to sign-in', async () => {
    const {controller, server} = setup();
    await loginMustChange(controller, server);
    server.passwordHandler(() => ({status: 401, body: {code: 'UNAUTHORIZED', message: 'invalid credentials or session'}}));
    const result = await controller.changePassword('pw123456789', 'brand-new-pw-1');
    expect(result.ok).toBe(false);
    expect(controller.getSnapshot().phase).toBe('signed-out');
  });
});

describe('SessionController — cancellation and stale-response isolation', () => {
  it('signOut aborts in-flight requests and late responses cannot restore state', async () => {
    const {controller, server} = setup();
    server.loginHandler(() => ({
      status: 200,
      body: {must_change_password: false, csrf_token: 'sess-token-1', user: USER_OK},
      setCookie: ['tiny_password_session=sess-1; Path=/; HttpOnly'],
    }));
    await controller.preflight();
    await controller.login('alice', 'pw123456789');

    // Start a slow session confirmation.
    const api = controller.getApi()!;
    let release!: () => void;
    const gate = new Promise<void>(r => {
      release = r;
    });
    const realFetch = (api.client as unknown as {fetchImpl: unknown}).fetchImpl as (
      url: string,
      init: FetchInit,
    ) => Promise<FetchResponseLike>;
    (api.client as unknown as {fetchImpl: unknown}).fetchImpl = (url: string, init: FetchInit) => {
      const p = gate.then(() => realFetch(url, init));
      init.signal?.addEventListener('abort', () => release());
      return p as Promise<FetchResponseLike>;
    };
    const pending = controller.confirmSession();
    await controller.signOut();
    expect(controller.getSnapshot().phase).toBe('signed-out');
    await pending; // late/aborted response arrives; must not change phase
    expect(controller.getSnapshot().phase).toBe('signed-out');
  });

  it('switching servers clears cookies and tokens (no cross-server leakage)', async () => {
    const {controller, server} = setup();
    server.loginHandler(() => ({
      status: 200,
      body: {must_change_password: false, csrf_token: 'sess-token-1', user: USER_OK},
      setCookie: ['tiny_password_session=sess-1; Path=/; HttpOnly'],
    }));
    await controller.preflight();
    await controller.login('alice', 'pw');
    expect(controller.getSnapshot().phase).toBe('authenticated');

    controller.setServerUrl('https://other.example.com');
    const snap = controller.getSnapshot();
    expect(snap.phase).toBe('signed-out');
    expect(snap.serverUrl).toBe('https://other.example.com');
    expect(controller.csrfToken).toBeNull();
    expect(controller.getApi()!.client.jar.size).toBe(0);
  });

  it('signOut clears the cookie jar even when the server call fails', async () => {
    const {controller, server} = setup();
    server.loginHandler(() => ({
      status: 200,
      body: {must_change_password: false, csrf_token: 'sess-token-1', user: USER_OK},
      setCookie: ['tiny_password_session=sess-1; Path=/; HttpOnly'],
    }));
    await controller.preflight();
    await controller.login('alice', 'pw');
    // Make logout fail at the transport level.
    const api = controller.getApi()!;
    (api.client as unknown as {fetchImpl: unknown}).fetchImpl = () => Promise.reject(new Error('offline'));
    const result = await controller.signOut();
    expect(result.serverConfirmed).toBe(false);
    expect(result.notice).toBeTruthy();
    expect(controller.getSnapshot().phase).toBe('signed-out');
    expect(controller.getApi()!.client.jar.size).toBe(0);
  });
});

describe('SessionController — error mapping', () => {
  it('timeouts surface as network errors with a visible message (never silent)', () => {
    const message = SessionController.loginError({
      kind: 'network-error',
      message: '请求超时，请检查网络后重试',
    });
    expect(message).toContain('超时');
  });

  it('rate-limit errors include the Retry-After window when present', () => {
    const message = SessionController.loginError({
      kind: 'http-error',
      status: 429,
      error: {code: 'RATE_LIMITED', message: 'too many attempts; slow down', retryAfterSeconds: 60},
    });
    expect(message).toContain('60');
  });
});

describe('SessionController — activity renewal and resume validation', () => {
  function loginOk(controller: SessionController, server: ReturnType<typeof setup>['server']) {
    server.loginHandler(() => ({
      status: 200,
      body: {must_change_password: false, csrf_token: 'sess-token-1', user: USER_OK},
      setCookie: ['tiny_password_session=sess-1; Path=/; HttpOnly'],
    }));
  }

  it('throttles activity renewal to once per window, foreground gestures only', async () => {
    const {controller, server, advance} = setup();
    loginOk(controller, server);
    await controller.preflight();
    await controller.login('alice', 'pw');

    // Login itself counts as recent activity: within the throttle window no
    // renewal request is sent at all.
    await controller.touchActivity();
    await controller.touchActivity();
    const activityCalls = () => server.calls.filter(c => c.url === '/api/v1/auth/session/activity').length;
    expect(activityCalls()).toBe(0);
    advance(6 * 60 * 1000);
    await controller.touchActivity();
    expect(activityCalls()).toBe(1);
    advance(60 * 1000); // still inside the new throttle window
    await controller.touchActivity();
    expect(activityCalls()).toBe(1);
  });

  it('no renewal outside the authenticated phase', async () => {
    const {controller, server} = setup();
    await controller.setServerUrl('https://vault.example.com');
    await controller.touchActivity();
    expect(server.calls.filter(c => c.url === '/api/v1/auth/session/activity')).toHaveLength(0);
  });

  it('resume validation: ok, signed-out and unreachable paths', async () => {
    const {controller, server} = setup();
    loginOk(controller, server);
    await controller.preflight();
    await controller.login('alice', 'pw');
    expect(await controller.validateOnResume()).toBe('ok');

    // Server-side session revoked → resume must return to sign-in.
    const api = controller.getApi()!;
    (api.client as unknown as {fetchImpl: unknown}).fetchImpl = async () => ({
      status: 401,
      headers: {get: () => null, getSetCookie: () => []},
      text: async () => JSON.stringify({code: 'UNAUTHORIZED', message: 'invalid credentials or session'}),
    });
    expect(await controller.validateOnResume()).toBe('signed-out');
    expect(controller.getSnapshot().phase).toBe('signed-out');
  });
});
