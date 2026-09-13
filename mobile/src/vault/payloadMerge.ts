import type {ItemDetail, LoginPayload} from '../api/types';
import {runeLength, utf8ByteLength} from '../api/text';

/**
 * Merge the edited fields back onto the ORIGINAL payload fetched from the
 * server. The two password date fields are never exposed in the mobile UI,
 * so they must survive verbatim; dropping them would silently clear data on
 * the server. The merged payload only ever contains fields the login payload
 * schema knows — the server rejects unknown fields.
 */
export function mergeLoginPayload(
  original: LoginPayload,
  edits: LoginEdits,
): LoginPayload {
  const merged: LoginPayload = {
    name: edits.name,
    username: edits.username,
    password: edits.password,
  };
  const urls = edits.urls.map(url => url.trim()).filter(url => url.length > 0);
  if (urls.length > 0) {
    merged.urls = urls;
  }
  if (edits.notes.length > 0) {
    merged.notes = edits.notes;
  }
  // Preserve fields the mobile UI does not show or edit.
  if (original.password_updated_at !== undefined) {
    merged.password_updated_at = original.password_updated_at;
  }
  if (original.password_expires_at !== undefined) {
    merged.password_expires_at = original.password_expires_at;
  }
  return merged;
}

export interface LoginEdits {
  name: string;
  username: string;
  password: string;
  urls: string[];
  notes: string;
}

export function editsFromDetail(detail: ItemDetail): LoginEdits {
  const p = detail.payload;
  return {
    name: p.name ?? '',
    username: p.username ?? '',
    password: p.password ?? '',
    urls: (p.urls ?? []).slice(),
    notes: p.notes ?? '',
  };
}

export function emptyEdits(): LoginEdits {
  return {name: '', username: '', password: '', urls: [''], notes: ''};
}

export interface FieldErrors {
  name?: string;
  username?: string;
  password?: string;
  urls?: string;
  notes?: string;
}

export const LIMITS = {
  nameRunes: 256,
  usernameRunes: 256,
  passwordBytes: 1024,
  urlCount: 16,
  urlRunes: 2048,
  notesRunes: 10000,
} as const;

/** Client-side mirror of the server's login payload validation. */
export function validateLoginEdits(edits: LoginEdits): FieldErrors {
  const errors: FieldErrors = {};
  const name = edits.name.trim();
  if (name.length === 0) {
    errors.name = '标题必填';
  } else if (runeLength(name) > LIMITS.nameRunes) {
    errors.name = `标题最多 ${LIMITS.nameRunes} 个字符`;
  }
  if (runeLength(edits.username) > LIMITS.usernameRunes) {
    errors.username = `用户名最多 ${LIMITS.usernameRunes} 个字符`;
  }
  if (utf8ByteLength(edits.password) > LIMITS.passwordBytes) {
    errors.password = `密码最多 ${LIMITS.passwordBytes} 字节（UTF-8）`;
  }
  const urls = edits.urls.filter(url => url.trim().length > 0);
  if (urls.length > LIMITS.urlCount) {
    errors.urls = `URL 最多 ${LIMITS.urlCount} 条`;
  }
  for (let i = 0; i < urls.length; i++) {
    if (runeLength(urls[i]) > LIMITS.urlRunes) {
      errors.urls = `第 ${i + 1} 条 URL 超过 ${LIMITS.urlRunes} 个字符`;
      break;
    }
  }
  if (runeLength(edits.notes) > LIMITS.notesRunes) {
    errors.notes = `备注最多 ${LIMITS.notesRunes} 个字符`;
  }
  return errors;
}

/**
 * Normalized URL list for saving: whitespace-only lines are dropped, kept
 * lines are trimmed, original order preserved.
 */
export function normalizedUrls(edits: LoginEdits): string[] {
  return edits.urls.map(url => url.trim()).filter(url => url.length > 0);
}
