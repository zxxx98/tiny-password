import {CookieJar, parseSetCookie, parseUrlParts, splitSetCookie} from '../src/api/cookies';

describe('parseUrlParts', () => {
  it('splits scheme, host and path', () => {
    expect(parseUrlParts('https://vault.example.com/api/v1/items')).toEqual({
      scheme: 'https',
      host: 'vault.example.com',
      path: '/api/v1/items',
    });
  });

  it('strips query and fragment from the path', () => {
    expect(parseUrlParts('http://h/x?a=1#f')!.path).toBe('/x');
  });

  it('lowercases the host and rejects non-http schemes', () => {
    expect(parseUrlParts('HTTPS://EXAMPLE.COM/a')!.host).toBe('example.com');
    expect(parseUrlParts('ftp://h/a')).toBeNull();
    expect(parseUrlParts('not a url')).toBeNull();
  });
});

describe('splitSetCookie', () => {
  it('splits joined headers on attribute boundaries', () => {
    const joined = [
      'a=1; Path=/; HttpOnly',
      'b=2; Path=/; Max-Age=60',
    ].join(', ');
    expect(splitSetCookie(joined)).toHaveLength(2);
    expect(splitSetCookie(joined)[0]).toContain('a=1');
    expect(splitSetCookie(joined)[1]).toContain('b=2');
  });

  it('keeps Expires dates with commas in one cookie', () => {
    const joined = 'sid=xyz; Expires=Wed, 21 Oct 2026 07:28:00 GMT; Path=/';
    expect(splitSetCookie(joined)).toHaveLength(1);
  });
});

describe('CookieJar', () => {
  const now = 1_000_000;
  const clock = () => now;
  const httpsUrl = 'https://vault.example.com/api/v1/items';

  it('stores and sends a host-only cookie', () => {
    const jar = new CookieJar(clock);
    jar.setFromResponse(httpsUrl, ['tiny_password_session=abc; Path=/; HttpOnly']);
    expect(jar.getCookieHeader(httpsUrl)).toBe('tiny_password_session=abc');
    // Same host, different path still matches Path=/.
    expect(jar.getCookieHeader('https://vault.example.com/api/v1/auth/session')).toBe(
      'tiny_password_session=abc',
    );
    // Different host must not match.
    expect(jar.getCookieHeader('https://other.example.com/api/v1/items')).toBeNull();
  });

  it('enforces the Secure flag', () => {
    const jar = new CookieJar(clock);
    jar.setFromResponse(httpsUrl, ['sid=1; Path=/; Secure']);
    expect(jar.getCookieHeader(httpsUrl)).toBe('sid=1');
    expect(jar.getCookieHeader('http://vault.example.com/api/v1/items')).toBeNull();
  });

  it('honours path scoping', () => {
    const jar = new CookieJar(clock);
    jar.setFromResponse(httpsUrl, ['scoped=1; Path=/api/v1']);
    expect(jar.getCookieHeader('https://vault.example.com/api/v1/items')).toBe('scoped=1');
    expect(jar.getCookieHeader('https://vault.example.com/other')).toBeNull();
  });

  it('expires via Max-Age', () => {
    let t = now;
    const jar = new CookieJar(() => t);
    jar.setFromResponse(httpsUrl, ['sid=1; Path=/; Max-Age=60']);
    expect(jar.getCookieHeader(httpsUrl)).toBe('sid=1');
    t = now + 61_000;
    expect(jar.getCookieHeader(httpsUrl)).toBeNull();
  });

  it('deletes on empty value or Max-Age<=0', () => {
    const jar = new CookieJar(clock);
    jar.setFromResponse(httpsUrl, ['sid=1; Path=/']);
    jar.setFromResponse(httpsUrl, ['sid=; Path=/; Max-Age=-1']);
    expect(jar.getCookieHeader(httpsUrl)).toBeNull();
    expect(jar.has('sid')).toBe(false);
  });

  it('never persists anything: a fresh jar is empty', () => {
    const jar = new CookieJar(clock);
    jar.setFromResponse(httpsUrl, ['sid=1; Path=/']);
    const fresh = new CookieJar(clock);
    expect(fresh.size).toBe(0);
    expect(fresh.getCookieHeader(httpsUrl)).toBeNull();
  });

  it('parseSetCookie computes the RFC 6265 default path', () => {
    const parts = parseUrlParts('https://h/a/b/c')!;
    expect(parseSetCookie('x=1', parts, now)!.path).toBe('/a/b');
    const root = parseUrlParts('https://h/')!;
    expect(parseSetCookie('x=1', root, now)!.path).toBe('/');
  });
});
