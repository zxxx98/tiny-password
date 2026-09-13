import {
  mergePayload,
  payloadFromEdits,
  validateEdits,
  editsFromDetail,
  emptyEdits,
  normalizedUrls,
  type CreditCardEdits,
  type IdentityEdits,
  type LoginEdits,
  type SecureNoteEdits,
  type SecretEdits,
  type SshKeyEdits,
} from '../src/vault/payloadMerge';
import {validateNewPassword, searchQueryError} from '../src/vault/validation';
import type {CreditCardPayload, ItemDetail, LoginPayload} from '../src/api/types';

describe('login payload merge — field preservation', () => {
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
    const edits: LoginEdits = {
      name: 'GitHub (work)',
      username: 'user@example.com',
      password: 'new-secret',
      urls: ['https://github.com'],
      notes: 'some notes',
    };
    const merged = mergePayload('login', original, edits) as LoginPayload;
    expect(merged.password_updated_at).toBe('2026-01-02');
    expect(merged.password_expires_at).toBe(null);
    expect(merged.name).toBe('GitHub (work)');
    expect(merged.password).toBe('new-secret');
  });

  it('never invents a urls/notes key when everything is empty', () => {
    const merged = mergePayload('login', {name: 'x', username: '', password: ''}, {
      name: 'x',
      username: '',
      password: '',
      urls: [''],
      notes: '',
    }) as LoginPayload;
    expect(merged.urls).toBeUndefined();
    expect(merged.notes).toBeUndefined();
  });

  it('filters whitespace-only URL lines and keeps order', () => {
    const merged = mergePayload('login', original, {
      name: 'n',
      username: '',
      password: '',
      urls: ['  https://a.dev  ', '', 'https://b.dev', '   '],
      notes: '',
    }) as LoginPayload;
    expect(merged.urls).toEqual(['https://a.dev', 'https://b.dev']);
  });

  it('drops unknown payload fields instead of echoing them', () => {
    const merged = mergePayload('login', original, {
      name: 'n',
      username: '',
      password: '',
      urls: [],
      notes: '',
    }) as LoginPayload;
    expect(Object.keys(merged).sort()).toEqual([
      'name',
      'password',
      'password_expires_at',
      'password_updated_at',
      'username',
    ]);
  });
});

describe('validateEdits — login', () => {
  const base: LoginEdits = {name: 'ok', username: '', password: '', urls: [''], notes: ''};

  it('requires a title', () => {
    expect(validateEdits('login', {...base, name: '   '}).name).toBeDefined();
  });

  it('counts Unicode characters, not bytes, for the title', () => {
    // 256 CJK runes = 768 bytes but exactly 256 characters: allowed.
    const title = '码'.repeat(256);
    expect(validateEdits('login', {...base, name: title}).name).toBeUndefined();
    expect(validateEdits('login', {...base, name: title + 'x'}).name).toBeDefined();
  });

  it('caps the item password at 1024 UTF-8 bytes', () => {
    const ok = 'a'.repeat(1024);
    expect(validateEdits('login', {...base, password: ok}).password).toBeUndefined();
    expect(validateEdits('login', {...base, password: ok + 'a'}).password).toBeDefined();
    const multibyte = 'ä'.repeat(513); // 1026 bytes, 513 chars
    expect(validateEdits('login', {...base, password: multibyte}).password).toBeDefined();
  });

  it('rejects more than 16 URLs', () => {
    const urls = Array.from({length: 17}, (_, i) => `https://x${i}.dev`);
    expect(validateEdits('login', {...base, urls}).urls).toBeDefined();
    expect(validateEdits('login', {...base, urls: urls.slice(0, 16)}).urls).toBeUndefined();
  });

  it('caps notes at 10000 characters', () => {
    expect(validateEdits('login', {...base, notes: 'a'.repeat(10000)}).notes).toBeUndefined();
    expect(validateEdits('login', {...base, notes: 'a'.repeat(10001)}).notes).toBeDefined();
  });
});

describe('validateEdits + merge — ssh_key', () => {
  const base: SshKeyEdits = {
    name: 'deploy key',
    algorithm: 'ed25519',
    public_key: 'ssh-ed25519 AAAA',
    private_key: '-----BEGIN OPENSSH PRIVATE KEY-----',
    key_passphrase: '',
    comment: '',
    fingerprint: '',
    notes: '',
  };

  it('accepts a valid edit set', () => {
    expect(validateEdits('ssh_key', base)).toEqual({});
  });

  it('requires public and private keys', () => {
    expect(validateEdits('ssh_key', {...base, public_key: '  '}).public_key).toBeDefined();
    expect(validateEdits('ssh_key', {...base, private_key: ''}).private_key).toBeDefined();
  });

  it('restricts the algorithm to the server enum', () => {
    expect(validateEdits('ssh_key', {...base, algorithm: 'rsa2048' as never}).algorithm).toBeDefined();
    expect(validateEdits('ssh_key', {...base, algorithm: 'rsa4096'}).algorithm).toBeUndefined();
  });

  it('caps public key at 8192 and private key at 16384 characters', () => {
    expect(validateEdits('ssh_key', {...base, public_key: 'a'.repeat(8193)}).public_key).toBeDefined();
    expect(validateEdits('ssh_key', {...base, private_key: 'a'.repeat(16385)}).private_key).toBeDefined();
    expect(validateEdits('ssh_key', {...base, public_key: 'a'.repeat(8192)}).public_key).toBeUndefined();
  });

  it('omits empty optional fields and preserves nothing extra on merge', () => {
    const merged = mergePayload('ssh_key', {...base, comment: 'keep me'}, {
      ...base,
      comment: '',
      fingerprint: '',
    });
    expect(merged).toEqual({
      name: base.name,
      algorithm: 'ed25519',
      public_key: base.public_key,
      private_key: base.private_key,
    });
  });
});

describe('validateEdits + merge — credit_card', () => {
  const base: CreditCardEdits = {
    name: 'main card',
    cardholder: 'ADA LOVELACE',
    number: '4242424242424242',
    exp_month: '12',
    exp_year: '2029',
    cvv: '123',
    pin: '',
    notes: '',
  };

  it('accepts a valid edit set', () => {
    expect(validateEdits('credit_card', base)).toEqual({});
  });

  it('requires cardholder and number', () => {
    expect(validateEdits('credit_card', {...base, cardholder: ' '}).cardholder).toBeDefined();
    expect(validateEdits('credit_card', {...base, number: ''}).number).toBeDefined();
  });

  it('bounds the expiry month and year to the server ranges', () => {
    expect(validateEdits('credit_card', {...base, exp_month: '0'}).exp_month).toBeDefined();
    expect(validateEdits('credit_card', {...base, exp_month: '13'}).exp_month).toBeDefined();
    expect(validateEdits('credit_card', {...base, exp_month: 'abc'}).exp_month).toBeDefined();
    expect(validateEdits('credit_card', {...base, exp_year: '1999'}).exp_year).toBeDefined();
    expect(validateEdits('credit_card', {...base, exp_year: '10000'}).exp_year).toBeDefined();
    expect(validateEdits('credit_card', {...base, exp_month: '1', exp_year: '2000'}).exp_month).toBeUndefined();
  });

  it('caps cvv at 8 and pin at 16 characters', () => {
    expect(validateEdits('credit_card', {...base, cvv: '123456789'}).cvv).toBeDefined();
    expect(validateEdits('credit_card', {...base, pin: 'a'.repeat(17)}).pin).toBeDefined();
  });

  it('emits numeric expiry fields and preserves the billing address reference', () => {
    const original: CreditCardPayload = {
      name: 'main card',
      cardholder: 'ADA LOVELACE',
      number: '4242424242424242',
      exp_month: 11,
      exp_year: 2028,
      billing_address_item_id: 'identity-1',
    };
    const merged = mergePayload('credit_card', original, base) as CreditCardPayload;
    expect(merged.exp_month).toBe(12);
    expect(merged.exp_year).toBe(2029);
    expect(merged.billing_address_item_id).toBe('identity-1');
    expect(merged.cvv).toBe('123');

    // A create payload has no reference to preserve.
    const fresh = payloadFromEdits('credit_card', base) as CreditCardPayload;
    expect(fresh.billing_address_item_id).toBeUndefined();
  });
});

describe('validateEdits — identity', () => {
  const base: IdentityEdits = {
    name: 'home address',
    full_name: 'Ada Lovelace',
    company: '',
    phone: '+86 138 0000 0000',
    email: 'ada@example.com',
    country: '中国',
    state: '北京',
    city: '北京',
    district: '海淀区',
    address_line: '中关村大街 1 号',
    postal_code: '100080',
    notes: '',
  };

  it('accepts a valid edit set with only the title required', () => {
    expect(validateEdits('identity', {name: 'x', full_name: '', company: '', phone: '', email: '', country: '', state: '', city: '', district: '', address_line: '', postal_code: '', notes: ''})).toEqual({});
  });

  it('caps per-field lengths mirroring the server', () => {
    expect(validateEdits('identity', {...base, email: 'a'.repeat(321)}).email).toBeDefined();
    expect(validateEdits('identity', {...base, phone: '1'.repeat(65)}).phone).toBeDefined();
    expect(validateEdits('identity', {...base, city: 'a'.repeat(97)}).city).toBeDefined();
    expect(validateEdits('identity', {...base, address_line: 'a'.repeat(513)}).address_line).toBeDefined();
    expect(validateEdits('identity', {...base, postal_code: '1'.repeat(33)}).postal_code).toBeDefined();
    expect(validateEdits('identity', {...base, full_name: 'a'.repeat(257)}).full_name).toBeDefined();
  });
});

describe('validateEdits — secure_note', () => {
  const base: SecureNoteEdits = {name: 'recovery notes', body: 'line one'};

  it('requires a non-blank body', () => {
    expect(validateEdits('secure_note', base)).toEqual({});
    expect(validateEdits('secure_note', {...base, body: '   '}).body).toBeDefined();
  });

  it('caps the body at 65536 characters', () => {
    expect(validateEdits('secure_note', {...base, body: 'a'.repeat(65536)}).body).toBeUndefined();
    expect(validateEdits('secure_note', {...base, body: 'a'.repeat(65537)}).body).toBeDefined();
  });

  it('round-trips the body through merge untouched', () => {
    const body = '密文\n第二行';
    const merged = mergePayload('secure_note', {name: 'n', body: 'old'}, {...base, body});
    expect(merged).toEqual({name: base.name, body});
  });
});

describe('validateEdits + merge — secret', () => {
  const base: SecretEdits = {
    name: 'api tokens',
    entries: [
      {key: 'STRIPE_KEY', value: 'sk_live_123'},
      {key: 'SMTP_PASS', value: 'hunter2'},
    ],
    notes: '',
  };

  it('accepts a valid edit set', () => {
    expect(validateEdits('secret', base)).toEqual({});
  });

  it('requires at least one entry with a non-blank key', () => {
    expect(validateEdits('secret', {...base, entries: []}).entries).toBeDefined();
    expect(validateEdits('secret', {...base, entries: [{key: '  ', value: 'v'}]})['entries.0.key']).toBeDefined();
  });

  it('rejects duplicate keys', () => {
    const errors = validateEdits('secret', {
      ...base,
      entries: [
        {key: 'A', value: '1'},
        {key: 'A', value: '2'},
      ],
    });
    expect(errors['entries.1.key']).toBeDefined();
  });

  it('caps entry count, key length and value length', () => {
    const many = Array.from({length: 129}, (_, i) => ({key: `k${i}`, value: 'v'}));
    expect(validateEdits('secret', {...base, entries: many}).entries).toBeDefined();
    expect(validateEdits('secret', {...base, entries: [{key: 'k'.repeat(257), value: 'v'}]})['entries.0.key']).toBeDefined();
    expect(validateEdits('secret', {...base, entries: [{key: 'k', value: 'v'.repeat(16385)}]})['entries.0.value']).toBeDefined();
  });

  it('preserves entry order through the merge', () => {
    const merged = mergePayload('secret', {name: 'api tokens', entries: [{key: 'A', value: '1'}, {key: 'B', value: '2'}]}, {
      name: 'api tokens',
      entries: [
        {key: 'B', value: '2-new'},
        {key: 'A', value: '1'},
      ],
      notes: 'kept',
    }) as {entries: {key: string; value: string}[]};
    expect(merged.entries.map(e => e.key)).toEqual(['B', 'A']);
    expect(merged.entries[0].value).toBe('2-new');
  });

  it('omits notes on create when empty', () => {
    const fresh = payloadFromEdits('secret', base) as {notes?: string};
    expect(fresh.notes).toBeUndefined();
  });
});

describe('editsFromDetail / emptyEdits per type', () => {
  it('maps a login detail onto the editor fields', () => {
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

  it('maps a credit card detail, stringifying the expiry numbers', () => {
    const detail = {
      id: '2',
      item_type: 'credit_card',
      vault_scope: 'personal',
      revision: 1,
      created_at: '',
      updated_at: '',
      favorite: false,
      payload: {name: 'c', cardholder: 'A', number: '42', exp_month: 4, exp_year: 2029},
    } as unknown as ItemDetail;
    const edits = editsFromDetail(detail) as CreditCardEdits;
    expect(edits.exp_month).toBe('4');
    expect(edits.exp_year).toBe('2029');
  });

  it('maps a secret detail onto entry rows', () => {
    const detail = {
      id: '3',
      item_type: 'secret',
      vault_scope: 'personal',
      revision: 1,
      created_at: '',
      updated_at: '',
      favorite: false,
      payload: {name: 's', entries: [{key: 'A', value: '1'}]},
    } as unknown as ItemDetail;
    expect(editsFromDetail(detail)).toEqual({
      name: 's',
      entries: [{key: 'A', value: '1'}],
      notes: '',
    });
  });

  it('emptyEdits seeds one URL and one secret entry row', () => {
    expect((emptyEdits('login') as LoginEdits).urls).toEqual(['']);
    expect((emptyEdits('secret') as SecretEdits).entries).toEqual([{key: '', value: ''}]);
    expect((emptyEdits('ssh_key') as SshKeyEdits).algorithm).toBe('ed25519');
    expect(emptyEdits('secure_note')).toEqual({name: '', body: ''});
  });

  it('normalizedUrls trims and drops blank lines', () => {
    expect(normalizedUrls({name: '', username: '', password: '', urls: [' a ', '', 'b'], notes: ''}))
      .toEqual(['a', 'b']);
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
