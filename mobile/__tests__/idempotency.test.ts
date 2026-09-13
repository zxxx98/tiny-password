import {
  canonicalCreateContent,
  createIdempotencyKeyManager,
  randomIdempotencyKey,
} from '../src/vault/idempotency';

describe('idempotency key reuse', () => {
  it('reuses the key for identical content (network retry)', () => {
    const mgr = createIdempotencyKeyManager();
    const content = JSON.stringify({name: 'a'});
    const first = mgr.keyFor(content);
    expect(mgr.keyFor(content)).toBe(first); // retry of the same logical create
  });

  it('uses a fresh key when the request content changes', () => {
    const mgr = createIdempotencyKeyManager();
    const first = mgr.keyFor(JSON.stringify({name: 'a'}));
    const second = mgr.keyFor(JSON.stringify({name: 'b'}));
    expect(second).not.toBe(first);
    expect(mgr.isBoundTo(JSON.stringify({name: 'b'}))).toBe(true);
  });

  it('reset() forces a new key even for identical content', () => {
    const mgr = createIdempotencyKeyManager();
    const content = JSON.stringify({name: 'a'});
    const first = mgr.keyFor(content);
    mgr.reset();
    expect(mgr.keyFor(content)).not.toBe(first);
  });

  it('keys match the server header constraints', () => {
    const key = randomIdempotencyKey();
    expect(key).toMatch(/^[A-Za-z0-9_-]{16,128}$/);
  });
});

describe('canonicalCreateContent', () => {
  it('is order-insensitive for object keys, order-sensitive for arrays', () => {
    const a = canonicalCreateContent({name: 'x', username: 'u'}, {item_type: 'login', vault_scope: 'personal'});
    const b = canonicalCreateContent({username: 'u', name: 'x'}, {item_type: 'login', vault_scope: 'personal'});
    expect(a).toBe(b);
    const c = canonicalCreateContent({urls: ['1', '2']}, {item_type: 'login', vault_scope: 'personal'});
    const d = canonicalCreateContent({urls: ['2', '1']}, {item_type: 'login', vault_scope: 'personal'});
    expect(c).not.toBe(d);
  });

  it('binds to the fixed type and scope', () => {
    const a = canonicalCreateContent({name: 'x'}, {item_type: 'login', vault_scope: 'personal'});
    const b = canonicalCreateContent({name: 'x'}, {item_type: 'login', vault_scope: 'shared'});
    expect(a).not.toBe(b);
  });
});
