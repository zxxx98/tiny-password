import { useEffect, useRef, useState } from "react";
import { request } from "../../app/api";
import { Button } from "../../design-system/Button";
import { scheduleClipboardCleanup } from "./clipboardCleanup";
import { errorText, type ItemDetail, type LoginPayload } from "./types";

export function QuickCopy({ itemId, title, csrfToken }: { itemId: string; title: string; csrfToken: string }) {
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState("");
  const controllerRef = useRef<AbortController | null>(null);
  const activeRef = useRef(false);
  const mountedRef = useRef(true);
  useEffect(() => {
    mountedRef.current = true;
    return () => { mountedRef.current = false; controllerRef.current?.abort(); };
  }, []);
  const copy = async (field: "username" | "password") => {
    if (activeRef.current) return;
    activeRef.current = true;
    const controller = new AbortController(); controllerRef.current = controller;
    setBusy(true); setStatus("");
    try {
      const detail = await request<ItemDetail>("GET", `/api/v1/items/${itemId}`, undefined, { csrfToken, signal: controller.signal });
      if (controller.signal.aborted) return;
      const value = (detail.payload as LoginPayload)[field];
      if (!value) { setStatus("该字段为空。"); return; }
      await navigator.clipboard.writeText(value);
      // Clipboard writes cannot be cancelled. Once successful, cleanup and
      // audit outlive this component. The audit still uses the API client's
      // global session cancellation, but ordinary navigation must not abort it.
      if (field === "password") void request("POST", `/api/v1/items/${itemId}/copy`, { field }, { csrfToken }).catch(() => {});
      if (mountedRef.current) setStatus("已复制；30 秒后尝试清除剪贴板。");
      void scheduleClipboardCleanup(value).then((result) => {
        if (mountedRef.current && controllerRef.current === controller && result === "unavailable") setStatus("已复制；浏览器不允许自动清除剪贴板。");
      });
    } catch (err) {
      if (mountedRef.current && !controller.signal.aborted) setStatus(err instanceof DOMException ? "复制失败，请打开详情手动复制。" : errorText(err).message);
    } finally { activeRef.current = false; if (mountedRef.current) setBusy(false); }
  };
  return <div role="group" aria-label={`${title} 的快捷复制`} className="flex flex-wrap items-center gap-1 px-2">
    <Button variant="ghost" className="px-2" disabled={busy} aria-label="复制用户名" onClick={() => void copy("username")}>复制账号</Button>
    <Button variant="ghost" className="px-2" disabled={busy} aria-label="复制密码" onClick={() => void copy("password")}>复制密码</Button>
    {status && <span role="status" className="max-w-48 font-body text-xs text-neutral-500">{status}</span>}
  </div>;
}
