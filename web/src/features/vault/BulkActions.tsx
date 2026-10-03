import { useEffect, useRef, useState } from "react";
import { ApiError } from "../../app/api";
import { Button } from "../../design-system/Button";
import { ConfirmDialog } from "../../design-system/Dialog";
import { applyAction, prepareAction, parseTags, actionError, type ItemAction, type PendingAction } from "./itemActions";
import { LIMITS, type ItemMeta } from "./types";

const labels: Record<ItemAction, string> = { trash: "批量移入回收站", personal: "批量移至个人", shared: "批量移至共享", tags: "批量添加标签", duplicate: "复制为个人条目" };

export function BulkActions({ items, csrfToken, canManage, onSucceeded, onChanged, onBusyChange, onClear, disabled = false }: {
  disabled?: boolean; items: ItemMeta[]; csrfToken: string; canManage: (item: ItemMeta) => boolean;
  onSucceeded: (id: string) => void; onChanged: () => void; onBusyChange: (busy: boolean) => void; onClear: () => void;
}) {
  const [action, setAction] = useState<ItemAction | null>(null);
  const [tagText, setTagText] = useState("");
  const [busy, setBusy] = useState(false);
  const [progress, setProgress] = useState("");
  const [errors, setErrors] = useState<string[]>([]);
  const pendingRef = useRef(new Map<string, PendingAction>());
  const controllerRef = useRef<AbortController | null>(null);
  const activeRef = useRef(false);
  useEffect(() => () => controllerRef.current?.abort(), []);
  useEffect(() => {
    if (!items.length && !activeRef.current) pendingRef.current.clear();
  }, [items.length]);
  const writable = items.every(canManage);
  const tags = parseTags(tagText);
  const validTags = tags.length > 0 && tags.length <= LIMITS.tagsCount && tags.every((tag) => Array.from(tag).length <= LIMITS.tag);
  const run = async () => {
    if (disabled || !items.length || !action || activeRef.current) return;
    activeRef.current = true;
    const controller = new AbortController(); controllerRef.current = controller;
    setBusy(true); onBusyChange(true); setErrors([]);
    let success = 0;
    const failures: string[] = [];
    try {
      for (const [index, item] of items.entries()) {
        if (controller.signal.aborted) return;
        setProgress(`处理中 ${index + 1}/${items.length}`);
        const operationID = JSON.stringify([item.id, action, tags]);
        try {
          let pending = pendingRef.current.get(operationID);
          if (!pending) {
            pending = await prepareAction(item, action, tags, csrfToken, controller.signal) ?? undefined;
            if (pending) pendingRef.current.set(operationID, pending);
          }
          if (controller.signal.aborted) return;
          if (pending) await applyAction(pending, csrfToken, controller.signal);
          if (controller.signal.aborted) return;
          pendingRef.current.delete(operationID);
          success++; onSucceeded(item.id);
        } catch (err) {
          if (controller.signal.aborted) return;
          failures.push(`${item.title}：${actionError(err)}`);
          // A known rejection can be prepared again; transport failures keep
          // the exact body/key so a committed create or move safely replays.
          if (err instanceof ApiError && err.status < 500 && err.code !== "CONFLICT") pendingRef.current.delete(operationID);
          if (err instanceof ApiError && err.status === 401) return;
        }
      }
      setErrors(failures);
      setProgress(`完成 ${success} 项${failures.length ? `，失败 ${failures.length} 项；失败条目仍保持选中。` : "。"}`);
      setAction(null);
      onChanged();
    } finally {
      activeRef.current = false;
      if (!controller.signal.aborted) { setBusy(false); onBusyChange(false); }
    }
  };
  if (!items.length && !progress && !errors.length) return null;
  return <section className="space-y-3 border border-ink p-3" aria-label="批量管理">
    <div className="flex flex-wrap items-center gap-2">
      <span className="font-body text-sm">已选 {items.length} 项</span>
      {Object.entries(labels).map(([key, label]) => <Button key={key} variant={key === "trash" ? "danger" : "secondary"} disabled={disabled || busy || items.length === 0 || (key !== "duplicate" && !writable)} onClick={() => setAction(key as ItemAction)}>{label}</Button>)}
      <Button variant="ghost" disabled={disabled || busy || items.length === 0} onClick={onClear}>取消选择</Button>
    </div>
    {items.length > 0 && !writable && <p className="font-body text-xs text-neutral-500">包含其他成员创建的共享条目，只能复制到个人保险库。</p>}
    {progress && <p role="status" className="font-body text-sm">{progress}</p>}
    {errors.length > 0 && <ul role="alert" className="font-body text-sm text-accent">{errors.map((error, index) => <li key={index}>{error}</li>)}</ul>}
    <ConfirmDialog open={action !== null} title={action ? labels[action] : "批量操作"} danger={action === "trash" || action === "shared" || action === "personal"}
      description={<div className="space-y-3"><p>将处理 {items.length} 个选中条目。每项独立执行，失败会保留以便重试。</p>
        {action === "trash" && <p>条目移入回收站，30 天内可恢复。</p>}
        {(action === "shared" || action === "personal") && <p>{action === "shared" ? "移至共享后所有成员都能读取。" : "移至个人后其他成员无法读取。"}移动会创建新条目并删除原条目，历史记录不保留；已经在目标保险库的条目会跳过。</p>}
        {action === "duplicate" && <p>副本保存到你的个人保险库，原条目保留。</p>}
        {action === "tags" && <><label className="block font-body text-sm" htmlFor="bulk-tags">添加标签（逗号分隔）</label><input id="bulk-tags" className="w-full border border-ink p-2" disabled={busy} value={tagText} onChange={(event) => setTagText(event.target.value)} /><p className="font-body text-xs">保留原有标签并自动去重；每个标签最多 64 个字符。</p></>}
      </div>}
      confirmLabel={busy ? "处理中…" : "确认执行"} confirmDisabled={disabled || !items.length || busy || (action === "tags" && !validTags)} onCancel={() => { if (!busy) setAction(null); }} onConfirm={() => void run()} />
  </section>;
}
