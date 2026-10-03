import { ApiError, request } from "../../app/api";
import { createIdempotencyKey } from "../../app/idempotency";
import { LIMITS, type ItemDetail, type ItemMeta } from "./types";

export type ItemAction = "trash" | "personal" | "shared" | "tags" | "duplicate";
export type PendingAction = { method: string; url: string; body: unknown; key: string };

export function parseTags(text: string): string[] {
  return [...new Set(text.split(/[,，]/).map((tag) => tag.trim()).filter(Boolean))];
}

export async function prepareAction(meta: ItemMeta, action: ItemAction, tags: string[], csrfToken: string, signal: AbortSignal): Promise<PendingAction | null> {
  const detail = await request<ItemDetail>("GET", `/api/v1/items/${meta.id}`, undefined, { csrfToken, signal });
  if (action !== "duplicate" && detail.revision !== meta.revision) {
    throw new Error("条目已更新，请刷新列表后重新选择。");
  }
  const key = createIdempotencyKey();
  if (action === "trash") return { method: "DELETE", url: `/api/v1/items/${meta.id}`, body: undefined, key };
  if (action === "duplicate") return {
    method: "POST", url: "/api/v1/items", key,
    body: { item_type: detail.item_type, vault_scope: "personal", tags: detail.tags, favorite: false,
      payload: { ...detail.payload, name: Array.from(detail.title).slice(0, LIMITS.name - 5).join("") + "（副本）" } },
  };
  if (action !== "tags" && detail.vault_scope === action) return null;
  return {
    method: "PUT", url: `/api/v1/items/${meta.id}`, key,
    body: { revision: meta.revision, payload: detail.payload, tags: action === "tags" ? [...new Set([...detail.tags, ...tags])] : detail.tags,
      favorite: detail.favorite, ...(action !== "tags" ? { vault_scope: action } : {}) },
  };
}

export async function applyAction(pending: PendingAction, csrfToken: string, signal: AbortSignal): Promise<ItemDetail | undefined> {
  return request<ItemDetail | undefined>(pending.method, pending.url, pending.body, { csrfToken, signal, idempotencyKey: pending.key });
}

export function actionError(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.code === "REVISION_CONFLICT") return "条目已更新，请刷新列表后重新选择。";
    if (err.code === "REFERENCE_FORBIDDEN") return "关联的身份地址无法在目标保险库使用，请先调整地址引用。";
    if (err.code === "FORBIDDEN") return "没有修改此条目的权限。";
    return err.message;
  }
  return err instanceof Error && !(err instanceof TypeError) ? err.message : "网络错误，请重试。";
}
