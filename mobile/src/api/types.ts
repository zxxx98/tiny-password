// API DTOs mirroring api/openapi.yaml. Only the subset the mobile MVP uses.

export type ItemType =
  | 'login'
  | 'ssh_key'
  | 'credit_card'
  | 'identity'
  | 'secure_note'
  | 'secret';

export type VaultScope = 'personal' | 'shared';

export interface CurrentPrincipal {
  user_id: string;
  username: string;
  role: 'admin' | 'member';
  must_change_password: boolean;
  idle_timeout_minutes: number;
  session: SessionInfo;
}

export interface SessionInfo {
  id: string;
  created_at: string;
  expires_at: string;
  idle_expires_at?: string;
  current: boolean;
}

export interface LoginPayload {
  name: string;
  username: string;
  password: string;
  urls?: string[];
  notes?: string;
  password_updated_at?: string | null;
  password_expires_at?: string | null;
}

export type SshAlgorithm = 'ed25519' | 'rsa4096';

export interface SshKeyPayload {
  name: string;
  algorithm: SshAlgorithm;
  public_key: string;
  private_key: string;
  key_passphrase?: string;
  comment?: string;
  fingerprint?: string;
  notes?: string;
}

export interface CreditCardPayload {
  name: string;
  cardholder: string;
  number: string;
  exp_month: number;
  exp_year: number;
  cvv?: string;
  pin?: string;
  billing_address_item_id?: string | null;
  notes?: string;
}

export interface IdentityPayload {
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
}

export interface SecureNotePayload {
  name: string;
  body: string;
}

export interface SecretEntry {
  key: string;
  value: string;
}

export interface SecretPayload {
  name: string;
  entries: SecretEntry[];
  notes?: string;
}

export type ItemPayload =
  | LoginPayload
  | SshKeyPayload
  | CreditCardPayload
  | IdentityPayload
  | SecureNotePayload
  | SecretPayload;

export interface ItemMeta {
  id: string;
  title?: string;
  item_type: ItemType;
  vault_scope: VaultScope;
  owner_id?: string | null;
  creator_id?: string | null;
  favorite: boolean;
  revision: number;
  created_at: string;
  updated_at: string;
  deleted_at?: string | null;
}

export interface ItemDetail extends ItemMeta {
  tags?: string[];
  payload: ItemPayload;
}

export interface CursorPage<T> {
  items: T[];
  next_cursor?: string | null;
}

export interface PasswordGeneratorOptions {
  length: number;
  lowercase: boolean;
  uppercase: boolean;
  digits: boolean;
  symbols: boolean;
  exclude_ambiguous: boolean;
}

export interface ApiErrorShape {
  code: string;
  message: string;
  request_id?: string;
  current_revision?: number;
}
