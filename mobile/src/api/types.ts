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
  payload: LoginPayload;
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
