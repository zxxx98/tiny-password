import { useCallback, useEffect, useState } from "react";
import { request } from "../../app/api";
import { Button } from "../../design-system/Button";
import { ConfirmDialog } from "../../design-system/Dialog";
import { ErrorSummary, Loading } from "../../design-system/Status";
import { TYPE_LABELS, errorText, type ItemMeta } from "./types";

export type TrashPageProps = {
  csrfToken: string;
  onChanged: () => void;
};

/**
 * TrashPage lists the caller's readable trashed items and offers restore and
 * early permanent deletion. Both stay policy-gated server-side.
 */
export function TrashPage({ csrfToken, onChanged }: TrashPageProps) {
  const [items, setItems] = useState<ItemMeta[] | null>(null);
  const [cursor, setCursor] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [requestId, setRequestId] = useState<string | undefined>();
  const [restoreTarget, setRestoreTarget] = useState<ItemMeta | null>(null);
  const [purgeTarget, setPurgeTarget] = useState<ItemMeta | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  const load = useCallback(
    async (nextCursor: string | null, append: boolean) => {
      try {
        const path = nextCursor ? `/api/v1/items/trash?cursor=${encodeURIComponent(nextCursor)}` : "/api/v1/items/trash";
        const page = await request<{ items: ItemMeta[]; next_cursor: string | null }>("GET", path, undefined, { csrfToken });
        setItems((prev) => (append && prev ? [...prev, ...page.items] : page.items));
        setCursor(page.next_cursor);
        setError(null);
      } catch (err) {
        const info = errorText(err);
        setError(info.message);
        setRequestId(info.requestId);
      }
    },
    [csrfToken],
  );

  useEffect(() => {
    void load(null, false);
  }, [load]);

  const act = async (item: ItemMeta, action: "restore" | "purge") => {
    setBusy(item.id + action);
    try {
      if (action === "restore") {
        await request("POST", `/api/v1/items/${item.id}/restore`, undefined, { csrfToken });
      } else {
        await request("DELETE", `/api/v1/items/${item.id}/purge`, undefined, { csrfToken });
      }
      setRestoreTarget(null);
      setPurgeTarget(null);
      await load(null, false);
      onChanged();
    } catch (err) {
      const info = errorText(err);
      setError(info.message);
      setRequestId(info.requestId);
    } finally {
      setBusy(null);
    }
  };

  return (
    <div className="mx-auto max-w-3xl px-4 py-10" aria-label="回收站">
      <header>
        <h2 className="font-display text-3xl font-bold">回收站</h2>
        <p className="mt-1 font-body text-sm text-neutral-600">
          条目在删除 30 天后由系统永久清除；在此可以提前恢复或永久删除。
        </p>
      </header>
      <div className="mt-6">
        <ErrorSummary message={error ?? ""} requestId={requestId} onDismiss={() => setError(null)} />
        {items === null ? (
          <Loading label="正在加载回收站…" />
        ) : items.length === 0 ? (
          <p className="font-body text-sm text-neutral-600">回收站是空的。</p>
        ) : (
          <ul className="divide-y divide-divider border-y border-divider">
            {items.map((item) => (
              <li key={item.id} className="flex flex-wrap items-center justify-between gap-3 py-3">
                <div>
                  <p className="font-body font-semibold">{item.title || "（无标题）"}</p>
                  <p className="mt-1 font-mono text-xs text-neutral-500">
                    {TYPE_LABELS[item.item_type]} · 删除于 {item.deleted_at?.slice(0, 19).replace("T", " ") ?? ""}Z
                  </p>
                </div>
                <div className="flex gap-2">
                  <Button variant="secondary" disabled={busy !== null} onClick={() => setRestoreTarget(item)}>
                    恢复
                  </Button>
                  <Button variant="danger" disabled={busy !== null} onClick={() => setPurgeTarget(item)}>
                    彻底删除
                  </Button>
                </div>
              </li>
            ))}
          </ul>
        )}
        {cursor && (
          <Button variant="ghost" className="mt-4" onClick={() => void load(cursor, true)}>加载更多</Button>
        )}
      </div>

      <ConfirmDialog
        open={restoreTarget !== null}
        title="恢复该条目？"
        description="条目会原样回到保险库，内容与版本号不变。"
        confirmLabel={busy !== null ? "恢复中…" : "恢复"}
        confirmDisabled={busy !== null}
        onCancel={() => { if (busy === null) setRestoreTarget(null); }}
        onConfirm={() => { if (restoreTarget && busy === null) void act(restoreTarget, "restore"); }}
      />
      <ConfirmDialog
        open={purgeTarget !== null}
        danger
        title="彻底删除？"
        description="条目及其全部历史版本将被永久清除，无法通过产品恢复。"
        confirmLabel={busy !== null ? "删除中…" : "彻底删除"}
        confirmDisabled={busy !== null}
        onCancel={() => { if (busy === null) setPurgeTarget(null); }}
        onConfirm={() => { if (purgeTarget && busy === null) void act(purgeTarget, "purge"); }}
      />
    </div>
  );
}
