import { useEffect, useRef, useState } from "react";
import { request } from "../../app/api";
import { createIdempotencyKey } from "../../app/idempotency";
import { Button } from "../../design-system/Button";
import { ErrorSummary } from "../../design-system/Status";
import { CreditCardFields, validateCreditCard } from "./forms/CreditCardFields";
import { IdentityFields, validateIdentity } from "./forms/IdentityFields";
import { LoginFields, validateLogin } from "./forms/LoginFields";
import { SecureNoteFields, validateSecureNote } from "./forms/SecureNoteFields";
import { SecretFields, validateSecret } from "./forms/SecretFields";
import { SshKeyFields, validateSshKey } from "./forms/SshKeyFields";
import {
  ITEM_TYPES,
  TYPE_LABELS,
  emptyPayload,
  errorText,
  type CreditCardPayload,
  type IdentityPayload,
  type ItemDetail as ItemDetailData,
  type ItemPayload,
  type ItemType,
  type LoginPayload,
  type SecureNotePayload,
  type SecretPayload,
  type SshKeyPayload,
} from "./types";

// The dispatch below guarantees the payload branch matches the validator.
const validators = {
  login: validateLogin,
  ssh_key: validateSshKey,
  credit_card: validateCreditCard,
  identity: validateIdentity,
  secure_note: validateSecureNote,
  secret: validateSecret,
} as const;

export type ItemEditorProps = {
  csrfToken: string;
  /** Present in edit mode; absent for creation. */
  initial?: ItemDetailData;
  /** Optional in-memory values handed off by the generator for a new login. */
  initialLoginDraft?: Partial<LoginPayload>;
  onSaved: (detail: ItemDetailData) => void;
  onCancel: () => void;
  onDirtyChange?: (dirty: boolean) => void;
  onBusyChange?: (busy: boolean) => void;
  onMoveConfirm?: (from: "personal" | "shared", to: "personal" | "shared") => Promise<boolean>;
};

function editorSnapshot(type: ItemType, scope: "personal" | "shared", payload: ItemPayload, tagText: string, favorite: boolean) {
  return JSON.stringify({ type, scope, payload, tagText, favorite });
}

/**
 * ItemEditor creates or updates one entry. Type and ownership are fixed by
 * the stored row in edit mode. On a 409 conflict the current edits are kept
 * on screen — the user chooses to reload, nothing is overwritten silently.
 */
export function ItemEditor({
  csrfToken,
  initial,
  initialLoginDraft,
  onSaved,
  onCancel,
  onDirtyChange,
  onBusyChange,
  onMoveConfirm,
}: ItemEditorProps) {
  const [type, setType] = useState<ItemType>(initial?.item_type ?? "login");
  const [scope, setScope] = useState<"personal" | "shared">(initial?.vault_scope ?? "personal");
  const [payload, setPayload] = useState<ItemPayload>(
    initial
      ? (initial.payload as ItemPayload)
      : ({ ...emptyPayload("login"), ...(initialLoginDraft ?? {}) } as ItemPayload),
  );
  const [tagText, setTagText] = useState(initial ? initial.tags.join(", ") : "");
  const [favorite, setFavorite] = useState(initial?.favorite ?? false);
  // The revision is part of the editor's authoritative snapshot. It must
  // advance when the user reloads after an optimistic-lock conflict.
  const [revision, setRevision] = useState(initial?.revision ?? 0);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [requestId, setRequestId] = useState<string | undefined>();
  const [conflict, setConflict] = useState<number | null>(null);
  const mountedRef = useRef(true);
  const conflictReloadRef = useRef<{ controller: AbortController; sequence: number } | null>(null);
  const conflictReloadSequenceRef = useRef(0);
  // One idempotency key per creation draft: double submits replay safely.
  const idempotencyKey = useRef<string>(createIdempotencyKey());
  // A move creates a new resource, so retries must reuse one key for this
  // editor draft to replay the same target instead of creating another one.
  const moveIdempotencyKey = useRef<string>(createIdempotencyKey());
  const initialSnapshot = useRef<string | null>(null);
  if (initialSnapshot.current === null) {
    initialSnapshot.current = editorSnapshot(type, scope, payload, tagText, favorite);
  }

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      conflictReloadRef.current?.controller.abort();
    };
  }, []);

  useEffect(() => {
    const dirty = editorSnapshot(type, scope, payload, tagText, favorite) !== initialSnapshot.current;
    onDirtyChange?.(dirty);
  }, [favorite, onDirtyChange, payload, scope, tagText, type]);

  useEffect(() => {
    onBusyChange?.(submitting);
  }, [onBusyChange, submitting]);

  const changeType = (next: ItemType) => {
    setType(next);
    setPayload(emptyPayload(next));
    setErrors({});
  };

  const submit = async (event: React.FormEvent) => {
    event.preventDefault();
    setError(null);
    setRequestId(undefined);
    const validator = validators[type];
    const clientErrors = validator(payload as never as never);
    setErrors(clientErrors as Record<string, string>);
    if (Object.keys(clientErrors).length > 0) {
      return;
    }
    const moving = Boolean(initial && scope !== initial.vault_scope);
    if (moving && initial && onMoveConfirm) {
      const confirmed = await onMoveConfirm(initial.vault_scope, scope);
      if (!confirmed || !mountedRef.current) return;
    }
    setSubmitting(true);
    const tags = tagText
      .split(/[,，]/)
      .map((t) => t.trim())
      .filter(Boolean);
    try {
      let saved: ItemDetailData;
      if (initial) {
        saved = await request<ItemDetailData>(
          "PUT",
          `/api/v1/items/${initial.id}`,
          { revision, payload, tags, favorite, ...(moving ? { vault_scope: scope } : {}) },
          { csrfToken, ...(moving ? { idempotencyKey: moveIdempotencyKey.current } : {}) },
        );
      } else {
        saved = await request<ItemDetailData>(
          "POST",
          "/api/v1/items",
          { item_type: type, vault_scope: scope, payload, tags, favorite },
          { csrfToken, idempotencyKey: idempotencyKey.current },
        );
      }
      initialSnapshot.current = editorSnapshot(
        saved.item_type,
        saved.vault_scope,
        saved.payload as ItemPayload,
        (saved.tags ?? []).join(", "),
        saved.favorite,
      );
      onDirtyChange?.(false);
      onSaved(saved);
    } catch (err) {
      const info = errorText(err);
      if (info.code === "REVISION_CONFLICT") {
        // Keep every edit on screen; never overwrite silently.
        setConflict(info.currentRevision ?? null);
      } else {
        setError(info.message);
        setRequestId(info.requestId);
      }
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <form onSubmit={submit} noValidate className="space-y-5 p-4 lg:p-6" aria-label={initial ? "编辑条目" : "新建条目"}>
      <h3 className="font-display text-2xl font-bold">{initial ? `编辑：${initial.title || "条目"}` : "新建条目"}</h3>

      <ErrorSummary message={error ?? ""} requestId={requestId} onDismiss={() => setError(null)} />

      {conflict !== null && (
        <div role="alert" className="border-l-4 border-accent bg-neutral-100 px-4 py-3">
          <p className="font-body text-sm">
            该条目已被其他人更新（当前版本 {conflict}）。你的编辑内容仍保留在下方。
          </p>
          <div className="mt-3 flex gap-2">
            <Button
              variant="secondary"
              onClick={() => {
                setConflict(null);
                // Reload authoritative content into the editor.
                if (initial) {
                  conflictReloadRef.current?.controller.abort();
                  const controller = new AbortController();
                  const sequence = ++conflictReloadSequenceRef.current;
                  conflictReloadRef.current = { controller, sequence };
                  void request<ItemDetailData>("GET", `/api/v1/items/${initial.id}`, undefined, { csrfToken, signal: controller.signal })
                    .then((fresh) => {
                      if (!mountedRef.current || controller.signal.aborted || conflictReloadRef.current?.sequence !== sequence) return;
                      const freshPayload = fresh.payload as ItemPayload;
                      const freshTags = (fresh.tags ?? []).join(", ");
                      const freshFavorite = typeof fresh.favorite === "boolean" ? fresh.favorite : favorite;
                      if (fresh.payload) {
                        setPayload(freshPayload);
                      }
                      setTagText(freshTags);
                      setFavorite(freshFavorite);
                      if (typeof fresh.revision === "number") {
                        setRevision(fresh.revision);
                      }
                      setScope(fresh.vault_scope);
                      initialSnapshot.current = editorSnapshot(fresh.item_type, fresh.vault_scope, freshPayload, freshTags, freshFavorite);
                      onDirtyChange?.(false);
                    })
                    .catch((err) => {
                      if (!mountedRef.current || controller.signal.aborted || conflictReloadRef.current?.sequence !== sequence) return;
                      const info = errorText(err);
                      setError(info.message);
                      setRequestId(info.requestId);
                    })
                    .finally(() => {
                      if (conflictReloadRef.current?.sequence === sequence) conflictReloadRef.current = null;
                    });
                }
              }}
            >
              重新加载服务端内容
            </Button>
            <Button variant="ghost" onClick={() => setConflict(null)}>
              继续编辑我的内容
            </Button>
          </div>
        </div>
      )}

      {!initial && (
        <>
          <div className="space-y-1">
            <label htmlFor="e-type" className="block font-mono text-xs uppercase tracking-widest">类型</label>
            <select
              id="e-type"
              value={type}
              onChange={(e) => changeType(e.target.value as ItemType)}
              className="min-h-[44px] w-full border-b-2 border-ink bg-transparent px-3 py-2 font-mono text-sm"
            >
              {ITEM_TYPES.map((t) => (
                <option key={t} value={t}>{TYPE_LABELS[t]}</option>
              ))}
            </select>
          </div>
          </>
        )}

      <fieldset>
        <legend className="font-mono text-xs uppercase tracking-widest">保存位置</legend>
        <div className="mt-2 flex gap-3">
          <label className="flex min-h-[44px] items-center gap-2 font-body text-sm">
            <input type="radio" name="scope" disabled={submitting} checked={scope === "personal"} onChange={() => setScope("personal")} />
            个人保险库
          </label>
          <label className="flex min-h-[44px] items-center gap-2 font-body text-sm">
            <input type="radio" name="scope" disabled={submitting} checked={scope === "shared"} onChange={() => setScope("shared")} />
            共享
          </label>
        </div>
      </fieldset>

      {type === "login" && <LoginFields payload={payload as LoginPayload} errors={errors} disabled={submitting} csrfToken={csrfToken} onChange={(patch) => setPayload({ ...payload, ...patch } as ItemPayload)} />}
      {type === "ssh_key" && <SshKeyFields payload={payload as SshKeyPayload} errors={errors} disabled={submitting} onChange={(patch) => setPayload({ ...payload, ...patch } as ItemPayload)} />}
      {type === "credit_card" && <CreditCardFields payload={payload as CreditCardPayload} errors={errors} disabled={submitting} onChange={(patch) => setPayload({ ...payload, ...patch } as ItemPayload)} />}
      {type === "identity" && <IdentityFields payload={payload as IdentityPayload} errors={errors} disabled={submitting} onChange={(patch) => setPayload({ ...payload, ...patch } as ItemPayload)} />}
      {type === "secure_note" && <SecureNoteFields payload={payload as SecureNotePayload} errors={errors} disabled={submitting} onChange={(patch) => setPayload({ ...payload, ...patch } as ItemPayload)} />}
      {type === "secret" && <SecretFields payload={payload as SecretPayload} errors={errors} disabled={submitting} onChange={(patch) => setPayload({ ...payload, ...patch } as ItemPayload)} />}

      <div className="space-y-4 border-t border-divider pt-4">
        <label htmlFor="e-tags" className="block font-mono text-xs uppercase tracking-widest">标签（逗号分隔，自动去重）</label>
        <input
          id="e-tags"
          value={tagText}
          onChange={(e) => setTagText(e.target.value)}
          className="min-h-[44px] w-full border-b-2 border-ink bg-transparent px-3 py-2 font-mono text-sm"
        />
        <label className="flex min-h-[44px] items-center gap-2 font-body text-sm">
          <input type="checkbox" checked={favorite} onChange={(e) => setFavorite(e.target.checked)} />
          收藏该条目
        </label>
      </div>

      <div className="flex gap-2">
        <Button type="submit" disabled={submitting}>
          {submitting ? "保存中…" : initial ? "保存修改" : "创建条目"}
        </Button>
        <Button variant="secondary" disabled={submitting} onClick={onCancel}>取消</Button>
      </div>
    </form>
  );
}
