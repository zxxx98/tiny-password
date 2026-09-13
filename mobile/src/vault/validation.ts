import {runeLength, utf8ByteLength} from '../api/text';

export const PASSWORD_RULES = {
  minChars: 12,
  maxBytes: 1024,
} as const;

/**
 * Client-side mirror of the account password policy (>=12 Unicode chars,
 * <=1024 UTF-8 bytes, no NUL). The new password must also differ from the
 * current one — the server enforces its own rules, this catches the obvious
 * mistakes before a rate-limited round trip.
 */
export function validateNewPassword(
  currentPassword: string,
  newPassword: string,
  confirmPassword: string,
): string | null {
  if (runeLength(newPassword) < PASSWORD_RULES.minChars) {
    return `新密码至少 ${PASSWORD_RULES.minChars} 个字符`;
  }
  if (utf8ByteLength(newPassword) > PASSWORD_RULES.maxBytes) {
    return `新密码最多 ${PASSWORD_RULES.maxBytes} 字节`;
  }
  if (newPassword.includes('\0')) {
    return '新密码不能包含空字符';
  }
  if (newPassword === currentPassword) {
    return '新密码不能与当前密码相同';
  }
  if (newPassword !== confirmPassword) {
    return '两次输入的新密码不一致';
  }
  return null;
}

export const SEARCH_MAX_RUNES = 256;

export function searchQueryError(query: string): string | null {
  if (runeLength(query) > SEARCH_MAX_RUNES) {
    return `搜索最多 ${SEARCH_MAX_RUNES} 个字符`;
  }
  return null;
}
