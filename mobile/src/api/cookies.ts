// In-process cookie jar. Cookies live only in memory for the life of the
// process — nothing here touches persistent storage, so a cold start always
// requires a fresh login (design §4.6). Matching covers scheme, exact host,
// path prefix and expiry, mirroring the constraints the server relies on
// (HttpOnly is enforced by the server side and irrelevant to a native client).

export interface StoredCookie {
  name: string;
  value: string;
  /** Exact request host the cookie was set by (host-only cookie). */
  host: string;
  path: string;
  secure: boolean;
  /** Absolute expiry in ms; null means a session cookie (process lifetime). */
  expiresAtMs: number | null;
}

export interface RequestUrlParts {
  scheme: string;
  host: string;
  path: string;
}

/** Parse an http(s) URL without depending on the URL global (Hermes). */
export function parseUrlParts(url: string): RequestUrlParts | null {
  const schemeMatch = /^https?:\/\//i.exec(url);
  if (!schemeMatch) {
    return null;
  }
  const scheme = schemeMatch[0].slice(0, -3).toLowerCase();
  const rest = url.slice(schemeMatch[0].length);
  const slash = rest.indexOf('/');
  const authority = slash === -1 ? rest : rest.slice(0, slash);
  let path = slash === -1 ? '/' : rest.slice(slash);
  if (path.length === 0) {
    path = '/';
  }
  // Strip query/fragment from the request path for cookie matching.
  const q = path.search(/[?#]/);
  if (q !== -1) {
    path = path.slice(0, q);
  }
  const at = authority.lastIndexOf('@');
  const hostPart = at === -1 ? authority : authority.slice(at + 1);
  if (!hostPart) {
    return null;
  }
  return {scheme, host: hostPart.toLowerCase(), path};
}

/**
 * Split a joined Set-Cookie header value into individual cookie strings.
 * RN joins multiple Set-Cookie headers with ", ". A comma only starts a new
 * cookie when the next token looks like "name=", which keeps Expires dates
 * (e.g. "Wed, 21 Oct 2026 ...") intact.
 */
export function splitSetCookie(joined: string): string[] {
  const out: string[] = [];
  let current = '';
  const parts = joined.split(',');
  for (let i = 0; i < parts.length; i++) {
    const part = i === 0 ? parts[i] : ',' + parts[i];
    if (i === 0) {
      current = part;
      continue;
    }
    const trimmed = part.replace(/^,/, '').trim();
    if (/^[^\s=;]+=\S*/.test(trimmed) && !/^expires=/i.test(trimmed)) {
      out.push(current.trim());
      current = part;
    } else {
      current += part;
    }
  }
  if (current.trim()) {
    out.push(current.trim());
  }
  return out;
}

export function parseSetCookie(
  raw: string,
  parts: RequestUrlParts,
  nowMs: number,
): StoredCookie | null {
  const segments = raw.split(';');
  const first = segments.shift();
  if (!first) {
    return null;
  }
  const eq = first.indexOf('=');
  if (eq === -1) {
    return null;
  }
  const name = first.slice(0, eq).trim();
  const value = first.slice(eq + 1).trim();
  if (!name) {
    return null;
  }
  let path = '';
  let secure = false;
  let expiresAtMs: number | null = null;
  for (const segment of segments) {
    const [attrRaw, ...rest] = segment.split('=');
    const attr = attrRaw.trim().toLowerCase();
    const attrValue = rest.join('=').trim();
    if (attr === 'path') {
      path = attrValue || '/';
    } else if (attr === 'secure') {
      secure = true;
    } else if (attr === 'max-age') {
      const seconds = Number(attrValue);
      if (Number.isFinite(seconds)) {
        if (seconds <= 0) {
          // Expired / deletion marker; keep the entry with an already-expired
          // timestamp so matching drops it and stale names get overwritten.
          expiresAtMs = nowMs - 1;
        } else {
          expiresAtMs = nowMs + seconds * 1000;
        }
      }
    } else if (attr === 'expires') {
      const parsed = Date.parse(attrValue);
      if (!Number.isNaN(parsed)) {
        expiresAtMs = parsed;
      }
    }
  }
  return {
    name,
    value,
    host: parts.host,
    path: path || defaultPath(parts.path),
    secure,
    expiresAtMs,
  };
}

// RFC 6265 default-path computation from the request URI.
function defaultPath(requestPath: string): string {
  if (!requestPath.startsWith('/')) {
    return '/';
  }
  const lastSlash = requestPath.lastIndexOf('/');
  if (lastSlash <= 0) {
    return '/';
  }
  return requestPath.slice(0, lastSlash);
}

function pathMatches(cookiePath: string, requestPath: string): boolean {
  if (cookiePath === '/') {
    return true;
  }
  if (!requestPath.startsWith(cookiePath)) {
    return false;
  }
  return (
    cookiePath.endsWith('/') || requestPath.charAt(cookiePath.length) === '/'
  );
}

export class CookieJar {
  private store = new Map<string, StoredCookie>();

  constructor(private readonly now: () => number = Date.now) {}

  /** Record every Set-Cookie payload from one response. Empty values delete. */
  setFromResponse(url: string, setCookieValues: string[]): void {
    const parts = parseUrlParts(url);
    if (!parts) {
      return;
    }
    for (const raw of setCookieValues) {
      const cookie = parseSetCookie(raw, parts, this.now());
      if (!cookie) {
        continue;
      }
      const key = this.keyFor(cookie.name, cookie.host, cookie.path);
      const expired = cookie.expiresAtMs !== null && cookie.expiresAtMs <= this.now();
      const emptyValue = cookie.value === '';
      if (expired || emptyValue) {
        // Only drop an existing entry; an empty/deleted cookie must not
        // create a fresh tombstone that would match future requests.
        if (this.store.has(key)) {
          this.store.delete(key);
        }
        continue;
      }
      this.store.set(key, cookie);
    }
  }

  /** Cookie header for a request URL, or null when nothing matches. */
  getCookieHeader(url: string): string | null {
    const parts = parseUrlParts(url);
    if (!parts) {
      return null;
    }
    const now = this.now();
    const matched: StoredCookie[] = [];
    for (const cookie of this.store.values()) {
      if (cookie.expiresAtMs !== null && cookie.expiresAtMs <= now) {
        continue;
      }
      if (cookie.secure && parts.scheme !== 'https') {
        continue;
      }
      if (cookie.host !== parts.host) {
        continue;
      }
      if (!pathMatches(cookie.path, parts.path)) {
        continue;
      }
      matched.push(cookie);
    }
    if (matched.length === 0) {
      return null;
    }
    // Longest path first for stable, spec-shaped ordering.
    matched.sort((a, b) => b.path.length - a.path.length);
    return matched.map(c => `${c.name}=${c.value}`).join('; ');
  }

  has(name: string): boolean {
    const now = this.now();
    for (const cookie of this.store.values()) {
      if (cookie.name === name && (cookie.expiresAtMs === null || cookie.expiresAtMs > now)) {
        return true;
      }
    }
    return false;
  }

  peek(name: string): string | null {
    const now = this.now();
    for (const cookie of this.store.values()) {
      if (cookie.name === name && (cookie.expiresAtMs === null || cookie.expiresAtMs > now)) {
        return cookie.value;
      }
    }
    return null;
  }

  clear(): void {
    this.store.clear();
  }

  get size(): number {
    return this.store.size;
  }

  private keyFor(name: string, host: string, path: string): string {
    return `${name}|${host}|${path}`;
  }
}
