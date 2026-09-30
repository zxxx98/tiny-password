import {CookieJar, parseUrlParts, splitSetCookie} from './cookies';
import {parseErrorEnvelope, type ApiError, type ApiResult, type HeadersLike} from './errors';
import type {
  CursorPage,
  CurrentPrincipal,
  ItemDetail,
  ItemMeta,
  ItemPayload,
  ItemType,
  PasswordGeneratorOptions,
} from './types';

export interface FetchLike {
  (url: string, init: FetchInit): Promise<FetchResponseLike>;
}

export interface FetchInit {
  method: string;
  headers: Record<string, string>;
  body?: string;
  signal?: AbortSignal;
  credentials?: 'omit';
}

export interface FetchResponseLike {
  status: number;
  headers: HeadersLike;
  text(): Promise<string>;
}

export interface RequestOptions {
  method: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE';
  /** Path under /api/v1, e.g. "/auth/login". */
  path: string;
  body?: unknown;
  csrfToken?: string | null;
  extraHeaders?: Record<string, string>;
  signal?: AbortSignal;
  timeoutMs?: number;
}

export const SESSION_COOKIE = 'tiny_password_session';
export const DEFAULT_TIMEOUT_MS = 20000;

export function normalizeServerUrl(input: string): string | null {
  const trimmed = input.trim().replace(/\/+$/, '');
  if (!trimmed) {
    return null;
  }
  const parts = parseUrlParts(trimmed);
  if (!parts || !parts.host) {
    return null;
  }
  // Rebuild the origin without any path: the API is served at the origin root.
  const schemeIndex = trimmed.toLowerCase().indexOf(parts.scheme);
  const authority = trimmed
    .slice(schemeIndex + parts.scheme.length + 3)
    .split('/')[0];
  if (!authority) {
    return null;
  }
  return `${parts.scheme}://${authority}`;
}

/**
 * HTTP client for the tiny-password API. Owns the in-memory cookie jar:
 * every response's Set-Cookie headers are recorded and every request sends
 * matching cookies. No cookie, token, password or entry is ever persisted.
 */
export class ApiClient {
  readonly jar: CookieJar;
  private readonly base: string;
  private readonly fetchImpl: FetchLike;
  private readonly onUnauthorized?: () => void;
  private readonly pending = new Set<AbortController>();

  constructor(
    serverUrl: string,
    options?: {fetchImpl?: FetchLike; jar?: CookieJar; now?: () => number; onUnauthorized?: () => void},
  ) {
    const normalized = normalizeServerUrl(serverUrl);
    if (!normalized) {
      throw new Error('服务器地址无效');
    }
    this.base = normalized;
    this.fetchImpl = options?.fetchImpl ?? defaultFetch;
    this.jar = options?.jar ?? new CookieJar(options?.now);
    this.onUnauthorized = options?.onUnauthorized;
  }

  /** Cancel every request, including requests made directly by screens. */
  cancelPending(): void {
    for (const controller of this.pending) {
      controller.abort();
    }
    this.pending.clear();
  }

  get serverUrl(): string {
    return this.base;
  }

  apiUrl(path: string): string {
    return `${this.base}/api/v1${path}`;
  }

  async request<T>(opts: RequestOptions): Promise<ApiResult<T>> {
    if (opts.signal?.aborted) {
      return {kind: 'aborted'};
    }
    const url = this.apiUrl(opts.path);
    const headers: Record<string, string> = {
      Accept: 'application/json',
      ...opts.extraHeaders,
    };
    const cookie = this.jar.getCookieHeader(url);
    if (cookie) {
      headers.Cookie = cookie;
    }
    if (opts.csrfToken) {
      headers['X-CSRF-Token'] = opts.csrfToken;
    }
    let body: string | undefined;
    if (opts.body !== undefined) {
      body = JSON.stringify(opts.body);
      headers['Content-Type'] = 'application/json';
    }

    const controller = new AbortController();
    this.pending.add(controller);
    let timeoutHandle: ReturnType<typeof setTimeout> | null = null;
    let timedOut = false;
    const abort = () => controller.abort();
    opts.signal?.addEventListener('abort', abort, {once: true});
    const timeoutMs = opts.timeoutMs ?? DEFAULT_TIMEOUT_MS;
    if (timeoutMs > 0) {
      timeoutHandle = setTimeout(() => {
        timedOut = true;
        controller.abort();
      }, timeoutMs);
    }

    try {
      const response = await this.fetchImpl(url, {
        method: opts.method,
        headers,
        body,
        signal: controller.signal,
        credentials: 'omit',
      });
      const raw = await response.text();
      // Some transports finish despite cancellation. Never accept their
      // cookies or data into a newer session, even after body consumption.
      if (controller.signal.aborted) {
        return timedOut
          ? {kind: 'network-error', message: '请求超时，请检查网络后重试'}
          : {kind: 'aborted'};
      }
      this.recordCookies(url, response.headers);
      if (response.status === 401 && opts.path !== '/auth/login') {
        this.onUnauthorized?.();
      }
      if (response.status >= 200 && response.status < 300) {
        try {
          const data = (raw.length === 0 ? undefined : JSON.parse(raw)) as T;
          return {kind: 'success', status: response.status, data, headers: response.headers};
        } catch {
          return {kind: 'http-error', status: response.status, error: parseErrorEnvelope(raw, response.status)};
        }
      }
      const error = parseErrorEnvelope(raw, response.status);
      const retryAfterSeconds = Number(response.headers.get('retry-after'));
      if (Number.isFinite(retryAfterSeconds) && retryAfterSeconds > 0) {
        error.retryAfterSeconds = Math.round(retryAfterSeconds);
      }
      return {kind: 'http-error', status: response.status, error};
    } catch (err) {
      if (controller.signal.aborted) {
        // A timeout must surface as a network error so the UI can show it;
        // only caller-initiated cancellation counts as a silent 'aborted'.
        if (timedOut) {
          return {
            kind: 'network-error',
            message: '请求超时，请检查网络后重试',
          };
        }
        return {kind: 'aborted'};
      }
      return {
        kind: 'network-error',
        message: err instanceof Error ? err.message : '网络请求失败',
      };
    } finally {
      this.pending.delete(controller);
      opts.signal?.removeEventListener('abort', abort);
      if (timeoutHandle !== null) {
        clearTimeout(timeoutHandle);
      }
    }

  }

  private recordCookies(url: string, headers: HeadersLike): void {
    let values: string[] = [];
    if (typeof headers.getSetCookie === 'function') {
      values = headers.getSetCookie();
    } else {
      const joined = headers.get('set-cookie');
      if (joined) {
        values = splitSetCookie(joined);
      }
    }
    if (values.length > 0) {
      this.jar.setFromResponse(url, values);
    }
  }
}

function defaultFetch(url: string, init: FetchInit): Promise<FetchResponseLike> {
  return fetch(url, init) as unknown as Promise<FetchResponseLike>;
}

// --- Typed API surface -------------------------------------------------------

/** Sensitive field categories the server's reveal/copy audit accepts. */
export type AuditField = 'password' | 'private_key' | 'key_passphrase' | 'number' | 'cvv' | 'pin';

export type LoginResult = {
  must_change_password: boolean;
  csrf_token: string;
  user: CurrentPrincipal;
};

export type SessionResult = {
  user: CurrentPrincipal;
  csrf_token: string;
};

export class TinyPasswordApi {
  constructor(readonly client: ApiClient) {}

  request<T>(opts: RequestOptions): Promise<ApiResult<T>> {
    return this.client.request<T>(opts);
  }

  issueCsrf(signal?: AbortSignal): Promise<ApiResult<{csrf_token: string}>> {
    return this.request({method: 'POST', path: '/csrf', signal});
  }

  login(
    username: string,
    password: string,
    csrfToken: string,
    signal?: AbortSignal,
  ): Promise<ApiResult<LoginResult>> {
    return this.request({
      method: 'POST',
      path: '/auth/login',
      body: {username, password},
      csrfToken,
      signal,
    });
  }

  getSession(signal?: AbortSignal): Promise<ApiResult<SessionResult>> {
    return this.request({method: 'GET', path: '/auth/session', signal});
  }

  logout(csrfToken: string, signal?: AbortSignal): Promise<ApiResult<void>> {
    return this.request({method: 'POST', path: '/auth/logout', csrfToken, signal});
  }

  changePassword(
    currentPassword: string,
    newPassword: string,
    csrfToken: string,
    signal?: AbortSignal,
  ): Promise<ApiResult<void>> {
    return this.request({
      method: 'POST',
      path: '/auth/password',
      body: {current_password: currentPassword, new_password: newPassword},
      csrfToken,
      signal,
    });
  }

  recordActivity(csrfToken: string, signal?: AbortSignal): Promise<ApiResult<void>> {
    return this.request({
      method: 'POST',
      path: '/auth/session/activity',
      csrfToken,
      signal,
    });
  }

  listItems(
    cursor?: string | null,
    limit = 50,
    signal?: AbortSignal,
  ): Promise<ApiResult<CursorPage<ItemMeta>>> {
    // No scope or type filter: the server returns this user's personal items
    // together with every shared item the session can read.
    let path = `/items?limit=${limit}`;
    if (cursor) {
      path += `&cursor=${encodeURIComponent(cursor)}`;
    }
    return this.request({method: 'GET', path, signal});
  }

  searchItems(
    query: string,
    cursor: string | null,
    csrfToken: string,
    signal?: AbortSignal,
    limit = 50,
  ): Promise<ApiResult<CursorPage<ItemMeta>>> {
    const body: Record<string, unknown> = {
      query,
      limit,
    };
    if (cursor) {
      body.cursor = cursor;
    }
    return this.request({method: 'POST', path: '/items/search', body, csrfToken, signal});
  }

  getItem(itemId: string, signal?: AbortSignal): Promise<ApiResult<ItemDetail>> {
    return this.request({method: 'GET', path: `/items/${encodeURIComponent(itemId)}`, signal});
  }

  createItem(
    payload: ItemPayload,
    itemType: ItemType,
    idempotencyKey: string,
    csrfToken: string,
    signal?: AbortSignal,
  ): Promise<ApiResult<ItemDetail>> {
    return this.request({
      method: 'POST',
      path: '/items',
      body: {item_type: itemType, vault_scope: 'personal', payload},
      csrfToken,
      extraHeaders: {'Idempotency-Key': idempotencyKey},
      signal,
    });
  }

  updateItem(
    itemId: string,
    revision: number,
    payload: ItemPayload,
    csrfToken: string,
    signal?: AbortSignal,
  ): Promise<ApiResult<ItemDetail>> {
    return this.request({
      method: 'PUT',
      path: `/items/${encodeURIComponent(itemId)}`,
      body: {revision, payload},
      csrfToken,
      signal,
    });
  }

  trashItem(itemId: string, csrfToken: string, signal?: AbortSignal): Promise<ApiResult<void>> {
    return this.request({
      method: 'DELETE',
      path: `/items/${encodeURIComponent(itemId)}`,
      csrfToken,
      signal,
    });
  }

  generatePassword(
    options: PasswordGeneratorOptions,
    csrfToken: string,
    signal?: AbortSignal,
  ): Promise<ApiResult<{value: string}>> {
    return this.request({
      method: 'POST',
      path: '/generators/password',
      body: options,
      csrfToken,
      signal,
    });
  }

  /** Sensitive field categories the server's audit trail accepts. */
  auditCopy(itemId: string, field: AuditField, csrfToken: string): Promise<ApiResult<void>> {
    return this.request({
      method: 'POST',
      path: `/items/${encodeURIComponent(itemId)}/copy`,
      body: {field},
      csrfToken,
    });
  }

  auditReveal(itemId: string, field: AuditField, csrfToken: string): Promise<ApiResult<void>> {
    return this.request({
      method: 'POST',
      path: `/items/${encodeURIComponent(itemId)}/reveal`,
      body: {field},
      csrfToken,
    });
  }

  static apiError(result: {kind: string; error?: ApiError}): ApiError | null {
    if (result.kind === 'http-error' && result.error) {
      return result.error;
    }
    return null;
  }
}
