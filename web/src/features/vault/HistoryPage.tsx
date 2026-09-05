import { useCallback, useEffect, useState } from "react";
import { request } from "../../app/api";
import { Button } from "../../design-system/Button";
import { ConfirmDialog } from "../../design-system/Dialog";
import { ErrorSummary, Loading } from "../../design-system/Status";
import { errorText, type HistoryEntry, type ItemDetail } from "./types";

export type HistoryPageProps = {
  itemId: string;
  csrfToken: string;
  onRestored: (detail: ItemDetail) => void;
  onClose: () => void;
};

/**
 * HistoryPage lists the encrypted history versions (metadata only) and
 * restores one as a new current revision. Restoring is a creator/owner-only
 * action; the server re-checks the policy.
 */
export function HistoryPage({ itemId, csrfToken, onRestored, onClose }: HistoryPageProps) {
  const [entries, setEntries] = useState<HistoryEntry[] | null>(null);
  const [cursor, setCursor] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [requestId, setRequestId] = useState<string | undefined>();
  const [restoring, setRestoring] = useState<number | null>(null);
  const [confirmRestore, setConfirmRestore] = useState<HistoryEntry | null>(null);

  const load = useCallback(
    async (nextCursor: string | null, append: boolean) => {
      try {
        const path = nextCursor
          ? `/api/v1/items/${itemId}/history?cursor=${encodeURIComponent(nextCursor)}`
          : `/api/v1/items/${itemId}/history`;
        const page = await request<{ items: HistoryEntry[]; next_cursor: string | null }>("GET", path, undefined, { csrfToken });
        setEntries((prev) => (append && prev ? [...prev, ...page.items] : page.items));
        setCursor(page.next_cursor);
        setError(null);
      } catch (err) {
        const info = errorText(err);
        setError(info.message);
        setRequestId(info.requestId);
      }
    },
    [itemId, csrfToken],
  );

  useEffect(() => {
    void load(null, false);
  }, [load]);

  const restore = async (revision: number) => {
    setRestoring(revision);
    try {
      const detail = await request<ItemDetail>(`POST`, `/api/v1/items/${itemId}/history/${revision}/restore`, {}, { csrfToken });
      onRestored(detail);
    } catch (err) {
      const info = errorText(err);
      setError(info.message);
      setRequestId(info.requestId);
    } finally {
      setRestoring(null);
      setConfirmRestore(null);
    }
  };

  return (
    <section className="space-y-5 p-4 lg:p-6" aria-label="历史版本">
      <header className="flex items-center justify-between">
        <h3 className="font-display text-2xl font-bold">历史版本</h3>
        <Button variant="ghost" onClick={onClose}>返回</Button>
      </header>
      <p className="font-body text-xs text-neutral-500">
        恢复会以全新加密创建一个新的当前版本，历史本身不会被改写。
      </p>
      <ErrorSummary message={error ?? ""} requestId={requestId} onDismiss={() => setError(null)} />
      {entries === null ? (
        <Loading label="正在加载历史…" />
      ) : entries.length === 0 ? (
        <p className="font-body text-sm text-neutral-600">还没有历史版本——首次有效更新后会自动保存。</p>
      ) : (
        <ul className="divide-y divide-divider border-y border-divider">
          {entries.map((entry) => (
            <li key={entry.revision} className="flex items-center justify-between gap-3 py-3">
              <p className="font-mono text-xs">
                版本 {entry.revision} · {entry.updated_at.slice(0, 19).replace("T", " ")}Z
              </p>
              <Button variant="secondary" disabled={restoring !== null} onClick={() => setConfirmRestore(entry)}>
                {restoring === entry.revision ? "恢复中…" : "恢复此版本"}
              </Button>
            </li>
          ))}
        </ul>
      )}
      {cursor && (
        <Button variant="ghost" onClick={() => void load(cursor, true)}>加载更多</Button>
      )}
      <ConfirmDialog
        open={confirmRestore !== null}
        title={`恢复版本 ${confirmRestore?.revision ?? ""}？`}
        description="会创建一个包含该版本内容的新当前版本；当前内容会先存入历史。"
        confirmLabel="恢复"
        onCancel={() => setConfirmRestore(null)}
        onConfirm={() => {
          if (confirmRestore) void restore(confirmRestore.revision);
        }}
      />
    </section>
  );
}
