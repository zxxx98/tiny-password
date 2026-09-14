import type {
  CreditCardPayload,
  IdentityPayload,
  ItemDetail,
  ItemPayload,
  ItemType,
  LoginPayload,
  SecureNotePayload,
  SecretPayload,
  SshAlgorithm,
  SshKeyPayload,
} from '../api/types';
import {runeLength, utf8ByteLength} from '../api/text';

/**
 * Per-type edit models and payload building. The editor edits flat string
 * fields; saving merges them onto the ORIGINAL payload fetched from the
 * server so fields the mobile UI never shows (login password dates, the
 * credit-card billing address reference) survive verbatim — dropping them
 * would silently clear data on the server. Merged payloads only ever carry
 * fields the server's payload schema knows — the server rejects unknown
 * fields.
 */

export interface LoginEdits {
  name: string;
  username: string;
  password: string;
  urls: string[];
  notes: string;
}

export interface SshKeyEdits {
  name: string;
  algorithm: SshAlgorithm;
  public_key: string;
  private_key: string;
  key_passphrase: string;
  comment: string;
  fingerprint: string;
  notes: string;
}

export interface CreditCardEdits {
  name: string;
  cardholder: string;
  number: string;
  /** Kept as UI strings; validated and converted to integers on save. */
  exp_month: string;
  exp_year: string;
  cvv: string;
  pin: string;
  notes: string;
}

export interface IdentityEdits {
  name: string;
  full_name: string;
  company: string;
  phone: string;
  email: string;
  country: string;
  state: string;
  city: string;
  district: string;
  address_line: string;
  postal_code: string;
  notes: string;
}

export interface SecureNoteEdits {
  name: string;
  body: string;
}

export interface SecretEntryEdits {
  key: string;
  value: string;
}

export interface SecretEdits {
  name: string;
  entries: SecretEntryEdits[];
  notes: string;
}

export type EntryEdits =
  | LoginEdits
  | SshKeyEdits
  | CreditCardEdits
  | IdentityEdits
  | SecureNoteEdits
  | SecretEdits;

export type FieldErrors = Record<string, string | undefined>;

export const LIMITS = {
  nameRunes: 256,
  shortTextRunes: 256,
  passwordBytes: 1024,
  urlCount: 16,
  urlRunes: 2048,
  notesRunes: 10000,
  publicKeyRunes: 8192,
  privateKeyRunes: 16384,
  passphraseRunes: 1024,
  fingerprintRunes: 128,
  cardNumberRunes: 64,
  cvvRunes: 8,
  pinRunes: 16,
  emailRunes: 320,
  phoneRunes: 64,
  regionRunes: 96,
  addressLineRunes: 512,
  postalCodeRunes: 32,
  noteBodyRunes: 65536,
  secretEntries: 128,
  secretKeyRunes: 256,
  secretValueRunes: 16384,
  expMonthMin: 1,
  expMonthMax: 12,
  expYearMin: 2000,
  expYearMax: 9999,
} as const;

export function emptyEdits(itemType: ItemType): EntryEdits {
  switch (itemType) {
    case 'login':
      return {name: '', username: '', password: '', urls: [''], notes: ''};
    case 'ssh_key':
      return {
        name: '',
        algorithm: 'ed25519',
        public_key: '',
        private_key: '',
        key_passphrase: '',
        comment: '',
        fingerprint: '',
        notes: '',
      };
    case 'credit_card':
      return {
        name: '',
        cardholder: '',
        number: '',
        exp_month: '',
        exp_year: '',
        cvv: '',
        pin: '',
        notes: '',
      };
    case 'identity':
      return {
        name: '',
        full_name: '',
        company: '',
        phone: '',
        email: '',
        country: '',
        state: '',
        city: '',
        district: '',
        address_line: '',
        postal_code: '',
        notes: '',
      };
    case 'secure_note':
      return {name: '', body: ''};
    case 'secret':
      return {name: '', entries: [{key: '', value: ''}], notes: ''};
  }
}

export function editsFromDetail(detail: ItemDetail): EntryEdits {
  const p = detail.payload;
  switch (detail.item_type) {
    case 'login': {
      const l = p as LoginPayload;
      return {
        name: l.name ?? '',
        username: l.username ?? '',
        password: l.password ?? '',
        urls: (l.urls ?? []).slice(),
        notes: l.notes ?? '',
      };
    }
    case 'ssh_key': {
      const k = p as SshKeyPayload;
      return {
        name: k.name ?? '',
        algorithm: k.algorithm ?? 'ed25519',
        public_key: k.public_key ?? '',
        private_key: k.private_key ?? '',
        key_passphrase: k.key_passphrase ?? '',
        comment: k.comment ?? '',
        fingerprint: k.fingerprint ?? '',
        notes: k.notes ?? '',
      };
    }
    case 'credit_card': {
      const c = p as CreditCardPayload;
      return {
        name: c.name ?? '',
        cardholder: c.cardholder ?? '',
        number: c.number ?? '',
        exp_month: c.exp_month === undefined ? '' : String(c.exp_month),
        exp_year: c.exp_year === undefined ? '' : String(c.exp_year),
        cvv: c.cvv ?? '',
        pin: c.pin ?? '',
        notes: c.notes ?? '',
      };
    }
    case 'identity': {
      const i = p as IdentityPayload;
      return {
        name: i.name ?? '',
        full_name: i.full_name ?? '',
        company: i.company ?? '',
        phone: i.phone ?? '',
        email: i.email ?? '',
        country: i.country ?? '',
        state: i.state ?? '',
        city: i.city ?? '',
        district: i.district ?? '',
        address_line: i.address_line ?? '',
        postal_code: i.postal_code ?? '',
        notes: i.notes ?? '',
      };
    }
    case 'secure_note': {
      const n = p as SecureNotePayload;
      return {name: n.name ?? '', body: n.body ?? ''};
    }
    case 'secret': {
      const s = p as SecretPayload;
      return {
        name: s.name ?? '',
        entries: (s.entries ?? []).map(e => ({key: e.key ?? '', value: e.value ?? ''})),
        notes: s.notes ?? '',
      };
    }
  }
}

/** Client-side mirror of the server's per-type payload validation. */
export function validateEdits(itemType: ItemType, edits: EntryEdits): FieldErrors {
  const errors: FieldErrors = {};
  const name = edits.name.trim();
  if (name.length === 0) {
    errors.name = '标题必填';
  } else if (runeLength(name) > LIMITS.nameRunes) {
    errors.name = `标题最多 ${LIMITS.nameRunes} 个字符`;
  }

  switch (itemType) {
    case 'login': {
      const e = edits as LoginEdits;
      if (runeLength(e.username) > LIMITS.shortTextRunes) {
        errors.username = `用户名最多 ${LIMITS.shortTextRunes} 个字符`;
      }
      if (utf8ByteLength(e.password) > LIMITS.passwordBytes) {
        errors.password = `密码最多 ${LIMITS.passwordBytes} 字节（UTF-8）`;
      }
      const urls = e.urls.filter(url => url.trim().length > 0);
      if (urls.length > LIMITS.urlCount) {
        errors.urls = `URL 最多 ${LIMITS.urlCount} 条`;
      }
      for (let i = 0; i < urls.length; i++) {
        if (runeLength(urls[i]) > LIMITS.urlRunes) {
          errors.urls = `第 ${i + 1} 条 URL 超过 ${LIMITS.urlRunes} 个字符`;
          break;
        }
      }
      if (runeLength(e.notes) > LIMITS.notesRunes) {
        errors.notes = `备注最多 ${LIMITS.notesRunes} 个字符`;
      }
      break;
    }
    case 'ssh_key': {
      const e = edits as SshKeyEdits;
      if (e.algorithm !== 'ed25519' && e.algorithm !== 'rsa4096') {
        errors.algorithm = '算法必须是 ed25519 或 rsa4096';
      }
      if (e.public_key.trim().length === 0) {
        errors.public_key = '公钥必填';
      } else if (runeLength(e.public_key) > LIMITS.publicKeyRunes) {
        errors.public_key = `公钥最多 ${LIMITS.publicKeyRunes} 个字符`;
      }
      if (e.private_key.trim().length === 0) {
        errors.private_key = '私钥必填';
      } else if (runeLength(e.private_key) > LIMITS.privateKeyRunes) {
        errors.private_key = `私钥最多 ${LIMITS.privateKeyRunes} 个字符`;
      }
      if (runeLength(e.key_passphrase) > LIMITS.passphraseRunes) {
        errors.key_passphrase = `口令最多 ${LIMITS.passphraseRunes} 个字符`;
      }
      if (runeLength(e.comment) > LIMITS.shortTextRunes) {
        errors.comment = `注释最多 ${LIMITS.shortTextRunes} 个字符`;
      }
      if (runeLength(e.fingerprint) > LIMITS.fingerprintRunes) {
        errors.fingerprint = `指纹最多 ${LIMITS.fingerprintRunes} 个字符`;
      }
      if (runeLength(e.notes) > LIMITS.notesRunes) {
        errors.notes = `备注最多 ${LIMITS.notesRunes} 个字符`;
      }
      break;
    }
    case 'credit_card': {
      const e = edits as CreditCardEdits;
      if (e.cardholder.trim().length === 0) {
        errors.cardholder = '持卡人必填';
      } else if (runeLength(e.cardholder) > LIMITS.shortTextRunes) {
        errors.cardholder = `持卡人最多 ${LIMITS.shortTextRunes} 个字符`;
      }
      if (e.number.trim().length === 0) {
        errors.number = '卡号必填';
      } else if (runeLength(e.number) > LIMITS.cardNumberRunes) {
        errors.number = `卡号最多 ${LIMITS.cardNumberRunes} 个字符`;
      }
      const month = parseExpNumber(e.exp_month);
      if (month === null || month < LIMITS.expMonthMin || month > LIMITS.expMonthMax) {
        errors.exp_month = `有效期月份必须是 ${LIMITS.expMonthMin}–${LIMITS.expMonthMax} 的整数`;
      }
      const year = parseExpNumber(e.exp_year);
      if (year === null || year < LIMITS.expYearMin || year > LIMITS.expYearMax) {
        errors.exp_year = `有效期年份必须是 ${LIMITS.expYearMin}–${LIMITS.expYearMax} 的整数`;
      }
      if (runeLength(e.cvv) > LIMITS.cvvRunes) {
        errors.cvv = `CVV 最多 ${LIMITS.cvvRunes} 个字符`;
      }
      if (runeLength(e.pin) > LIMITS.pinRunes) {
        errors.pin = `PIN 最多 ${LIMITS.pinRunes} 个字符`;
      }
      if (runeLength(e.notes) > LIMITS.notesRunes) {
        errors.notes = `备注最多 ${LIMITS.notesRunes} 个字符`;
      }
      break;
    }
    case 'identity': {
      const e = edits as IdentityEdits;
      if (runeLength(e.full_name) > LIMITS.shortTextRunes) {
        errors.full_name = `姓名最多 ${LIMITS.shortTextRunes} 个字符`;
      }
      if (runeLength(e.company) > LIMITS.shortTextRunes) {
        errors.company = `公司最多 ${LIMITS.shortTextRunes} 个字符`;
      }
      if (runeLength(e.phone) > LIMITS.phoneRunes) {
        errors.phone = `电话最多 ${LIMITS.phoneRunes} 个字符`;
      }
      if (runeLength(e.email) > LIMITS.emailRunes) {
        errors.email = `邮箱最多 ${LIMITS.emailRunes} 个字符`;
      }
      for (const field of ['country', 'state', 'city', 'district'] as const) {
        if (runeLength(e[field]) > LIMITS.regionRunes) {
          errors[field] = `最多 ${LIMITS.regionRunes} 个字符`;
        }
      }
      if (runeLength(e.address_line) > LIMITS.addressLineRunes) {
        errors.address_line = `地址最多 ${LIMITS.addressLineRunes} 个字符`;
      }
      if (runeLength(e.postal_code) > LIMITS.postalCodeRunes) {
        errors.postal_code = `邮编最多 ${LIMITS.postalCodeRunes} 个字符`;
      }
      if (runeLength(e.notes) > LIMITS.notesRunes) {
        errors.notes = `备注最多 ${LIMITS.notesRunes} 个字符`;
      }
      break;
    }
    case 'secure_note': {
      const e = edits as SecureNoteEdits;
      if (e.body.trim().length === 0) {
        errors.body = '正文必填';
      } else if (runeLength(e.body) > LIMITS.noteBodyRunes) {
        errors.body = `正文最多 ${LIMITS.noteBodyRunes} 个字符`;
      }
      break;
    }
    case 'secret': {
      const e = edits as SecretEdits;
      if (e.entries.length < 1 || e.entries.length > LIMITS.secretEntries) {
        errors.entries = `至少 1 条、最多 ${LIMITS.secretEntries} 条键值对`;
      }
      const seen = new Set<string>();
      e.entries.forEach((entry, i) => {
        if (entry.key.trim().length === 0) {
          errors[`entries.${i}.key`] = '键名必填';
        } else if (runeLength(entry.key) > LIMITS.secretKeyRunes) {
          errors[`entries.${i}.key`] = `键名最多 ${LIMITS.secretKeyRunes} 个字符`;
        } else if (seen.has(entry.key)) {
          errors[`entries.${i}.key`] = '键名重复';
        } else {
          seen.add(entry.key);
        }
        if (runeLength(entry.value) > LIMITS.secretValueRunes) {
          errors[`entries.${i}.value`] = `值最多 ${LIMITS.secretValueRunes} 个字符`;
        }
      });
      if (runeLength(e.notes) > LIMITS.notesRunes) {
        errors.notes = `备注最多 ${LIMITS.notesRunes} 个字符`;
      }
      break;
    }
  }
  return errors;
}

function parseExpNumber(raw: string): number | null {
  const trimmed = raw.trim();
  if (!/^\d+$/.test(trimmed)) {
    return null;
  }
  const value = Number(trimmed);
  return Number.isSafeInteger(value) ? value : null;
}

/**
 * Normalized URL list for saving: whitespace-only lines are dropped, kept
 * lines are trimmed, original order preserved.
 */
export function normalizedUrls(edits: LoginEdits): string[] {
  return edits.urls.map(url => url.trim()).filter(url => url.length > 0);
}

/** Build a fresh create payload from edits only (no original to preserve). */
export function payloadFromEdits(itemType: ItemType, edits: EntryEdits): ItemPayload {
  switch (itemType) {
    case 'login': {
      const e = edits as LoginEdits;
      const payload: LoginPayload = {
        name: e.name,
        username: e.username,
        password: e.password,
      };
      const urls = normalizedUrls(e);
      if (urls.length > 0) {
        payload.urls = urls;
      }
      if (e.notes.length > 0) {
        payload.notes = e.notes;
      }
      return payload;
    }
    case 'ssh_key': {
      const e = edits as SshKeyEdits;
      const payload: SshKeyPayload = {
        name: e.name,
        algorithm: e.algorithm,
        public_key: e.public_key,
        private_key: e.private_key,
      };
      if (e.key_passphrase.length > 0) {
        payload.key_passphrase = e.key_passphrase;
      }
      if (e.comment.length > 0) {
        payload.comment = e.comment;
      }
      if (e.fingerprint.length > 0) {
        payload.fingerprint = e.fingerprint;
      }
      if (e.notes.length > 0) {
        payload.notes = e.notes;
      }
      return payload;
    }
    case 'credit_card': {
      const e = edits as CreditCardEdits;
      const payload: CreditCardPayload = {
        name: e.name,
        cardholder: e.cardholder,
        number: e.number,
        exp_month: parseExpNumber(e.exp_month) ?? 0,
        exp_year: parseExpNumber(e.exp_year) ?? 0,
      };
      if (e.cvv.length > 0) {
        payload.cvv = e.cvv;
      }
      if (e.pin.length > 0) {
        payload.pin = e.pin;
      }
      if (e.notes.length > 0) {
        payload.notes = e.notes;
      }
      return payload;
    }
    case 'identity': {
      const e = edits as IdentityEdits;
      const payload: IdentityPayload = {name: e.name};
      for (const field of [
        'full_name',
        'company',
        'phone',
        'email',
        'country',
        'state',
        'city',
        'district',
        'address_line',
        'postal_code',
      ] as const) {
        if (e[field].length > 0) {
          payload[field] = e[field];
        }
      }
      if (e.notes.length > 0) {
        payload.notes = e.notes;
      }
      return payload;
    }
    case 'secure_note': {
      const e = edits as SecureNoteEdits;
      return {name: e.name, body: e.body};
    }
    case 'secret': {
      const e = edits as SecretEdits;
      const payload: SecretPayload = {
        name: e.name,
        entries: e.entries.map(entry => ({key: entry.key, value: entry.value})),
      };
      if (e.notes.length > 0) {
        payload.notes = e.notes;
      }
      return payload;
    }
  }
}

/**
 * Merge the edited fields back onto the ORIGINAL payload fetched from the
 * server, preserving fields the mobile UI does not show or edit:
 * login password dates and the credit-card billing address reference.
 */
export function mergePayload(itemType: ItemType, original: ItemPayload, edits: EntryEdits): ItemPayload {
  const payload = payloadFromEdits(itemType, edits);
  if (itemType === 'login') {
    const o = original as LoginPayload;
    const m = payload as LoginPayload;
    if (o.password_updated_at !== undefined) {
      m.password_updated_at = o.password_updated_at;
    }
    if (o.password_expires_at !== undefined) {
      m.password_expires_at = o.password_expires_at;
    }
  } else if (itemType === 'credit_card') {
    const o = original as CreditCardPayload;
    const m = payload as CreditCardPayload;
    if (o.billing_address_item_id !== undefined) {
      m.billing_address_item_id = o.billing_address_item_id;
    }
  }
  return payload;
}
