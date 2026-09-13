import {
  mergeLoginPayload,
  validateLoginEdits,
  editsFromDetail,
  normalizedUrls,
  type LoginEdits,
} from '../src/vault/payloadMerge';
import {validateNewPassword, searchQueryError} from '../src/vault/validation';
import type {ItemDetail, LoginPayload} from '../src/api/types';

describe('mergeLoginPayload — field preservation', () => {
  const original: LoginPayload = {
    name: 'GitHub',
    username: 'user@example.com',
    password: 'old-secret',
    urls: ['https://github.com'],
    notes: 'some notes',
    password_updated_at: '2026-01-02',
    password_expires_at: null,
  };

  it('preserves password dates the UI never shows', () => {
    const merged = mergeLoginPayload(original, {
      name: 'GitHub (work)',
      username: 'user@example.com',
      password: 'new-secret',
      urls: ['https://github.com'],
      notes: 'some notes',
    });
    expect(merged.password_updated_at).toBe('2026-01-02');
    expect(merged.password_expires_at).toBe(null);
    expect(merged.name).toBe('GitHub (work)');
    expect(merged.password).toBe('new-secret');
  });

  it('never invents a urls/notes key when everything is empty', () => {
    const merged = mergeLoginPayload(
      {name: 'x', username: '', password: ''},
      {name: 'x', username: '', password: '', urls: [''], notes: ''},
    );
    expect(merged.urls).toBeUndefined();
    expect(merged.notes).toBeUndefined();
  });

  it('filters whitespace-only URL lines and keeps order', () => {
    const merged = mergeLoginPayload(original, {
      name: 'n',
      username: '',
      password: '',
      urls: ['  https://a.dev  ', '', 'https://b.dev', '   '],
      notes: '',
    });
    expect(merged.urls).toEqual(['https://a.dev', 'https://b.dev']);
  });

  it('drops unknown payload fields instead of echoing them', () => {
    const merged = mergeLoginPayload(original, {
      name: 'n',
      username: '',
      password: '',
      urls: [],
      notes: '',
    });
    expect(Object.keys(merged).sort()).toEqual([
      'name',
      'password',
      'password_expires_at',
      'password_updated_at',
      'username',
    ]);
  });
});

describe('validateLoginEdits', () => {
  const base: LoginEdits = {name: 'ok', username: '', password: '', urls: [''], notes: ''};

  it('requires a title', () => {
    expect(validateLoginEdits({...base, name: '   '}).name).toBeDefined();
  });

  it('counts Unicode characters, not bytes, for the title', () => {
    // 256 CJK runes = 768 bytes but exactly 256 characters: allowed.
    const title = '码'.repeat(256);
    expect(validateLoginEdits({...base, name: title}).name).toBeUndefined();
    expect(validateLoginEdits({...base, name: title + 'x'}).name).toBeDefined();
  });

  it('caps the item password at 1024 UTF-8 bytes', () => {
    const ok = 'a'.repeat(1024);
    expect(validateLoginEdits({...base, password: ok}).password).toBeUndefined();
    expect(validateLoginEdits({...base, password: ok + 'a'}).password).toBeDefined();
    const multibyte = 'ä'.repeat(513); // 1026 bytes, 513 chars
    expect(validateLoginEdits({...base, password: multibyte}).password).toBeDefined();
  });

  it('rejects more than 16 URLs', () => {
    const urls = Array.from({length: 17}, (_, i) => `https://x${i}.dev`);
    expect(validateLoginEdits({...base, urls}).urls).toBeDefined();
    expect(validateLoginEdits({...base, urls: urls.slice(0, 16)}).urls).toBeUndefined();
  });

  it('caps notes at 10000 characters', () => {
    expect(validateLoginEdits({...base, notes: 'a'.repeat(10000)}).notes).toBeUndefined();
    expect(validateLoginEdits({...base, notes: 'a'.repeat(10001)}).notes).toBeDefined();
  });
});

describe('validateNewPassword', () => {
  it('requires 12+ characters', () => {
    expect(validateNewPassword('current-pass-1', 'short12pass', 'short12pass')).toContain('12');
  });

  it('caps at 1024 bytes', () => {
    expect(validateNewPassword('c', 'a'.repeat(1025), 'a'.repeat(1025))).toContain('1024');
  });

  it('rejects a new password equal to the current one', () => {
    expect(validateNewPassword('same-password-123', 'same-password-123', 'same-password-123')).toContain(
      '相同',
    );
  });

  it('requires confirmation to match (client-only check)', () => {
    expect(validateNewPassword('current-pass-1', 'new-password-1', 'new-password-2')).toContain(
      '不一致',
    );
  });

  it('accepts a valid change', () => {
    expect(validateNewPassword('current-pass-1', 'new-password-1', 'new-password-1')).toBeNull();
  });
});

describe('search limits', () => {
  it('allows at most 256 Unicode characters', () => {
    expect(searchQueryError('码'.repeat(256))).toBeNull();
    expect(searchQueryError('码'.repeat(257))).toContain('256');
  });
});

describe('editsFromDetail', () => {
  it('maps a detail onto the editor fields', () => {
    const detail = {
      id: '1',
      item_type: 'login',
      vault_scope: 'personal',
      revision: 3,
      created_at: '',
      updated_at: '',
      favorite: false,
      payload: {name: 'A', username: 'u', password: 'p', urls: ['https://a'], notes: 'n'},
    } as unknown as ItemDetail;
    expect(editsFromDetail(detail)).toEqual({
      name: 'A',
      username: 'u',
      password: 'p',
      urls: ['https://a'],
      notes: 'n',
    });
  });

  it('normalizedUrls trims and drops blank lines', () => {
    expect(normalizedUrls({name: '', username: '', password: '', urls: [' a ', '', 'b'], notes: ''}))
      .toEqual(['a', 'b']);
  });
});
