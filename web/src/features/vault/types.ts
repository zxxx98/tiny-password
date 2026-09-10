import type { ApiError } from "../../app/api";

/** The six fixed item types of V1 (design §6.2). */
export const ITEM_TYPES = ["login", "ssh_key", "credit_card", "identity", "secure_note", "secret"] as const;
export type ItemType = (typeof ITEM_TYPES)[number];

export const TYPE_LABELS: Record<ItemType, string> = {
  login: "登录凭据",
  ssh_key: "SSH 密钥",
  credit_card: "银行卡",
  identity: "身份地址",
  secure_note: "安全笔记",
  secret: "密钥",
};

export type LoginPayload = {
  name: string;
  username?: string;
  password?: string;
  urls?: string[];
  notes?: string;
  password_updated_at?: string | null;
  password_expires_at?: string | null;
};

export type SshKeyPayload = {
  name: string;
  algorithm: "ed25519" | "rsa4096";
  public_key?: string;
  private_key?: string;
  key_passphrase?: string;
  comment?: string;
  fingerprint?: string;
  notes?: string;
};

export type CreditCardPayload = {
  name: string;
  cardholder: string;
  number: string;
  exp_month: number;
  exp_year: number;
  cvv?: string;
  pin?: string;
  billing_address_item_id?: string | null;
  notes?: string;
};

export type IdentityPayload = {
  name: string;
  full_name?: string;
  company?: string;
  phone?: string;
  email?: string;
  country?: string;
  state?: string;
  city?: string;
  district?: string;
  address_line?: string;
  postal_code?: string;
  notes?: string;
};

export type SecureNotePayload = {
  name: string;
  body: string;
};

export type SecretEntry = {
  key: string;
  value: string;
};

export type SecretPayload = {
  name: string;
  entries: SecretEntry[];
  notes?: string;
};

export type ItemPayload = LoginPayload | SshKeyPayload | CreditCardPayload | IdentityPayload | SecureNotePayload | SecretPayload;

export type ItemMeta = {
  id: string;
  title: string;
  item_type: ItemType;
  vault_scope: "personal" | "shared";
  owner_id?: string;
  creator_id?: string;
  creator_name?: string;
  favorite: boolean;
  revision: number;
  created_at: string;
  updated_at: string;
  deleted_at: string | null;
};

export type ItemDetail = ItemMeta & {
  tags: string[];
  payload: ItemPayload;
};

export type HistoryEntry = { revision: number; updated_at: string };

export type HealthReport = {
  weak: number;
  reused: number;
  expired: number;
  items: Array<{ item_id: string; reasons: string[] }>;
};

/** Sensitive field categories audited on reveal/copy (design §6.4). */
export const SENSITIVE_FIELDS = ["password", "key_passphrase", "private_key", "cvv", "pin", "number"] as const;
export type SensitiveField = (typeof SENSITIVE_FIELDS)[number];

/** Client mirror of the server's field limits for inline validation. */
export const LIMITS = {
  name: 256,
  username: 256,
  notes: 10000,
  urls: 16,
  url: 2048,
  publicKey: 8192,
  privateKey: 16384,
  passphrase: 1024,
  comment: 256,
  fingerprint: 128,
  cardNumber: 64,
  cvv: 8,
  pin: 16,
  email: 320,
  phone: 64,
  region: 96,
  addressLine: 512,
  postalCode: 32,
  noteBody: 65536,
  secretEntries: 128,
  secretKey: 256,
  secretValue: 16384,
  tagsCount: 32,
  tag: 64,
} as const;

/** Builds a fresh payload template per type for the editor. */
export function emptyPayload(type: ItemType): ItemPayload {
  switch (type) {
    case "login":
      return { name: "", username: "", password: "", urls: [], notes: "" };
    case "ssh_key":
      return { name: "", algorithm: "ed25519", public_key: "", private_key: "", key_passphrase: "", comment: "", fingerprint: "", notes: "" };
    case "credit_card":
      return { name: "", cardholder: "", number: "", exp_month: 1, exp_year: new Date().getFullYear(), cvv: "", pin: "", billing_address_item_id: null, notes: "" };
    case "identity":
      return { name: "", full_name: "", company: "", phone: "", email: "", country: "", state: "", city: "", district: "", address_line: "", postal_code: "", notes: "" };
    case "secure_note":
      return { name: "", body: "" };
    case "secret":
      return { name: "", entries: [{ key: "", value: "" }], notes: "" };
  }
}

/** Shared client-side length check mirroring the server's code-point limits. */
export function overLimit(value: string | undefined, max: number): string | undefined {
  if (value === undefined) {
    return undefined;
  }
  return Array.from(value).length > max ? `最多 ${max} 个字符。` : undefined;
}

/** Extracts the safe, display-oriented ApiError information for a page. */
export function errorText(err: unknown): { message: string; requestId?: string; code?: string; currentRevision?: number } {
  const apiError = err as ApiError;
  if (apiError && typeof apiError === "object" && "code" in apiError) {
    return {
      message: apiError.message,
      requestId: apiError.requestId,
      code: apiError.code,
      currentRevision: apiError.currentRevision,
    };
  }
  return { message: "网络错误，请重试。" };
}

export function describeItem(meta: ItemMeta): string {
  if (meta.vault_scope === "shared") {
    return `共享 · 创建者 ${meta.creator_name ?? "未知成员"}`;
  }
  return "个人";
}
