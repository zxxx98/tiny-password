import { useCallback, useEffect, useRef, useState } from "react";
import { request } from "../../app/api";
import { Button } from "../../design-system/Button";
import { ConfirmDialog } from "../../design-system/Dialog";
import { ErrorSummary, Loading } from "../../design-system/Status";
import { errorText, type HistoryEntry, type ItemDetail } from "./types";

export type HistoryPageProps = {
  itemId: string;
  csrfToken: string;
  canManage: boolean;
  onRestored: (detail: ItemDetail) => void;
  onClose: () => void;
  onBusyChange?: (busy: boolean) => void;
};

/**
 * HistoryPage lists the encrypted history versions (metadata only) and
 * restores one as a new current revision. Restoring is a creator/owner-only
 * action; the server re-checks the policy.
 */
export function HistoryPage({ itemId, csrfToken, canManage, onRestored, onClose, onBusyChange }: HistoryPageProps) {
  const [entries, setEntries] = useState<HistoryEntry[] | null>(null);
  const [cursor, setCursor] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [requestId, setRequestId] = useState<string | undefined>();
  const [restoring, setRestoring] = useState<number | null>(null);
  const [confirmRestore, setConfirmRestore] = useState<HistoryEntry | null>(null);
  const mountedRef = useRef(true);
  const loadRequestRef = useRef<{ controller: AbortController; sequence: number } | null>(null);
  const restoreRequestRef = useRef<{ controller: AbortController; sequence: number } | null>(null);
  const requestSequenceRef = useRef(0);

  useEffect(() => {
    return () => {
      mountedRef.current = false;
      loadRequestRef.current?.controller.abort();
      restoreRequestRef.current?.controller.abort();
      onBusyChange?.(false);
    };
  }, [onBusyChange]);

  const load = useCallback(
    async (nextCursor: string | null, append: boolean) => {
      loadRequestRef.current?.controller.abort();
      const controller = new AbortController();
      const sequence = ++requestSequenceRef.current;
      loadRequestRef.current = { controller, sequence };
      try {
        const path = nextCursor
          ? `/api/v1/items/${itemId}/history?cursor=${encodeURIComponent(nextCursor)}`
          : `/api/v1/items/${itemId}/history`;
        const page = await request<{ items: HistoryEntry[]; next_cursor: string | null }>("GET", path, undefined, { csrfToken, signal: controller.signal });
        if (!mountedRef.current || controller.signal.aborted || loadRequestRef.current?.sequence !== sequence) return;
        setEntries((prev) => (append && prev ? [...prev, ...page.items] : page.items));
        setCursor(page.next_cursor);
        setError(null);
      } catch (err) {
        if (!mountedRef.current || controller.signal.aborted || loadRequestRef.current?.sequence !== sequence) return;
        const info = errorText(err);
        setError(info.message);
        setRequestId(info.requestId);
      } finally {
        if (loadRequestRef.current?.sequence === sequence) loadRequestRef.current = null;
      }
    },
    [itemId, csrfToken],
  );

  useEffect(() => {
    void load(null, false);
  }, [load]);

  const restore = async (revision: number) => {
    if (restoring !== null) return;
    const controller = new AbortController();
    const sequence = ++requestSequenceRef.current;
    restoreRequestRef.current = { controller, sequence };
    setRestoring(revision);
    onBusyChange?.(true);
    try {
      const detail = await request<ItemDetail>(`POST`, `/api/v1/items/${itemId}/history/${revision}/restore`, {}, { csrfToken, signal: controller.signal });
      if (!mountedRef.current || controller.signal.aborted || restoreRequestRef.current?.sequence !== sequence) return;
      onRestored(detail);
    } catch (err) {
      if (!mountedRef.current || controller.signal.aborted || restoreRequestRef.current?.sequence !== sequence) return;
      const info = errorText(err);
      setError(info.message);
      setRequestId(info.requestId);
    } finally {
      if (restoreRequestRef.current?.sequence === sequence) restoreRequestRef.current = null;
      if (mountedRef.current) {
        setRestoring(null);
        setConfirmRestore(null);
        onBusyChange?.(false);
      }
    }
  };

  return (
    <section className="space-y-5 p-4 lg:p-6" aria-label="历史版本">
      <header className="flex items-center justify-between">
        <h3 className="font-display text-2xl font-bold">历史版本</h3>
        <Button variant="ghost" disabled={restoring !== null} onClick={onClose}>返回</Button>
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
              {canManage ? (
                <Button variant="secondary" disabled={restoring !== null} onClick={() => setConfirmRestore(entry)}>
                  {restoring === entry.revision ? "恢复中…" : "恢复此版本"}
                </Button>
              ) : (
                <span className="font-mono text-xs text-neutral-500">仅可查看</span>
              )}
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
        confirmLabel={restoring !== null ? "恢复中…" : "恢复"}
        confirmDisabled={restoring !== null}
        onCancel={() => { if (restoring === null) setConfirmRestore(null); }}
        onConfirm={() => {
          if (confirmRestore && restoring === null) void restore(confirmRestore.revision);
        }}
      />
    </section>
  );
}
