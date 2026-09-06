import { useCallback, useEffect, useState } from "react";
import { ApiError, request } from "../../app/api";
import { useSession } from "../../app/session";
import { Button } from "../../design-system/Button";
import { ErrorSummary } from "../../design-system/Status";

type AuditEntry = {
  id: string;
  event: string;
  actor_id: string | null;
  target_type: string | null;
  target_id: string | null;
  result: "success" | "failure";
  request_id: string | null;
  created_at: string;
};

type Page = { items: AuditEntry[]; next_cursor: string | null };

const eventOptions = [
  { value: "", label: "全部事件" },
  { value: "auth.login.success", label: "登录成功" },
  { value: "auth.login.failure", label: "登录失败" },
  { value: "auth.password.changed", label: "密码修改" },
  { value: "user.created", label: "成员创建" },
  { value: "user.disabled", label: "成员禁用" },
  { value: "user.deleted", label: "成员删除" },
  { value: "vault.item.created", label: "条目创建" },
  { value: "vault.item.updated", label: "条目更新" },
  { value: "vault.item.viewed", label: "条目查看" },
  { value: "backup.retention.deleted", label: "备份清理" },
  { value: "transfer.exported", label: "个人导出" },
  { value: "transfer.imported", label: "个人导入" },
];

const resultOptions = [
  { value: "", label: "全部结果" },
  { value: "success", label: "成功" },
  { value: "failure", label: "失败" },
];

/**
 * AuditPage: the admin system-audit view with event/result filters (T27).
 * Rows are redacted server-side — opaque ids only, never values.
 */
export function AuditPage() {
  const { csrfToken } = useSession();
  const [entries, setEntries] = useState<AuditEntry[] | null>(null);
  const [cursor, setCursor] = useState<string | null>(null);
  const [event, setEvent] = useState("");
  const [result, setResult] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [requestId, setRequestId] = useState<string | undefined>();

  const fail = useCallback((err: unknown) => {
    if (err instanceof ApiError) {
      setError(err.message);
      setRequestId(err.requestId);
    } else {
      setError("网络错误，请重试。");
    }
  }, []);

  const load = useCallback(
    async (nextCursor: string | null, append: boolean) => {
      try {
        const params = new URLSearchParams();
        if (event) {
          params.set("event", event);
        }
        if (result) {
          params.set("result", result);
        }
        if (nextCursor) {
          params.set("cursor", nextCursor);
        }
        const page = await request<Page>(
          "GET",
          `/api/v1/admin/audit${params.toString() ? `?${params.toString()}` : ""}`,
          undefined,
          { csrfToken },
        );
        setEntries((prev) => (append && nextCursor ? [...(prev ?? []), ...(page.items ?? [])] : (page.items ?? [])));
        setCursor(page.next_cursor ?? null);
        setError(null);
      } catch (err) {
        fail(err);
      }
    },
    [csrfToken, event, result, fail],
  );

  useEffect(() => {
    void load(null, false);
  }, [load]);

  return (
    <div className="mx-auto max-w-3xl px-4 py-10">
      <header>
        <h2 className="font-display text-3xl font-bold">系统审计</h2>
        <p className="mt-1 font-body text-sm text-neutral-600">
          全部记录均为脱敏事件：操作者与目标只显示不透明 ID，不含任何敏感值。
        </p>
      </header>

      <div className="mt-6">
        <ErrorSummary message={error ?? ""} requestId={requestId} onDismiss={() => setError(null)} />
      </div>

      <form
        className="mt-4 flex flex-wrap items-end gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          void load(null, false);
        }}
      >
        <label className="font-body text-sm">
          事件
          <select
            aria-label="事件筛选"
            className="mt-1 block border border-ink bg-white px-2 py-1.5"
            value={event}
            onChange={(e) => setEvent(e.target.value)}
          >
            {eventOptions.map((o) => (
              <option key={o.value} value={o.value}>
                {o.label}
              </option>
            ))}
          </select>
        </label>
        <label className="font-body text-sm">
          结果
          <select
            aria-label="结果筛选"
            className="mt-1 block border border-ink bg-white px-2 py-1.5"
            value={result}
            onChange={(e) => setResult(e.target.value)}
          >
            {resultOptions.map((o) => (
              <option key={o.value} value={o.value}>
                {o.label}
              </option>
            ))}
          </select>
        </label>
        <Button type="submit">应用筛选</Button>
      </form>

      {entries === null ? (
        <p className="mt-6 font-body text-sm text-neutral-600">正在加载审计记录…</p>
      ) : entries.length === 0 ? (
        <p className="mt-6 font-body text-sm text-neutral-600">当前筛选没有匹配的审计事件。</p>
      ) : (
        <ul className="mt-6 divide-y divide-divider border-y border-divider">
          {entries.map((entry) => (
            <li key={entry.id} className="py-3">
              <p className="font-body text-sm">
                <span className="font-mono text-xs">{entry.event}</span>
                <span className={`ml-2 font-mono text-xs ${entry.result === "failure" ? "text-red-700" : "text-neutral-500"}`}>
                  {entry.result === "failure" ? "失败" : "成功"}
                </span>
              </p>
              <p className="mt-1 font-mono text-xs text-neutral-500">
                {entry.created_at.slice(0, 19).replace("T", " ")}
                {entry.actor_id && ` · 操作者 ${entry.actor_id.slice(0, 8)}…`}
                {entry.target_id && ` · 目标 ${entry.target_id.slice(0, 12)}…`}
                {entry.request_id && ` · 请求 ${entry.request_id.slice(0, 8)}…`}
              </p>
            </li>
          ))}
        </ul>
      )}
      {cursor && (
        <Button variant="secondary" className="mt-4" onClick={() => void load(cursor, true)}>
          加载更多
        </Button>
      )}
    </div>
  );
}
