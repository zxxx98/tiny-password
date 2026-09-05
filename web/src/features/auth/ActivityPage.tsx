import { useCallback, useEffect, useState } from "react";
import { ApiError, request } from "../../app/api";
import { Button } from "../../design-system/Button";
import { ErrorSummary } from "../../design-system/Status";

type AuditEntry = {
  id: string;
  event: string;
  actor_id?: string;
  target_type?: string;
  target_id?: string;
  result: "success" | "failure";
  request_id?: string;
  created_at: string;
};

type Page = { items: AuditEntry[]; next_cursor: string | null };

const eventLabels: Record<string, string> = {
  "setup.success": "初始化成功",
  "setup.failure": "初始化失败",
  "auth.login.success": "登录成功",
  "auth.login.failure": "登录失败",
  "auth.logout": "退出登录",
  "auth.session.revoked": "会话撤销",
  "auth.password.changed": "密码修改",
  "user.created": "成员创建",
  "user.disabled": "成员停用",
  "user.enabled": "成员启用",
  "user.deleted": "成员删除",
  "user.sessions.revoked": "会话批量撤销",
  "vault.item.created": "条目创建",
  "vault.item.updated": "条目更新",
  "vault.item.viewed": "条目查看",
  "vault.item.trashed": "条目入回收站",
  "vault.item.restored": "条目恢复",
  "vault.item.purged": "条目永久删除",
  "vault.item.history_restored": "历史恢复",
};

/**
 * ActivityPage: the caller's own redacted audit events (D08). Opaque ids
 * only — the server never sends usernames, titles or values.
 */
export function ActivityPage() {
  const [entries, setEntries] = useState<AuditEntry[] | null>(null);
  const [cursor, setCursor] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [requestId, setRequestId] = useState<string | undefined>();

  const load = useCallback(async (nextCursor: string | null, append: boolean) => {
    try {
      const path = nextCursor
        ? `/api/v1/auth/activity?cursor=${encodeURIComponent(nextCursor)}`
        : "/api/v1/auth/activity";
      const page = await request<Page>("GET", path, undefined, {
        csrfToken: undefined,
      });
      setEntries((prev) => (append && prev ? [...prev, ...page.items] : page.items));
      setCursor(page.next_cursor);
      setError(null);
    } catch (err) {
      if (err instanceof ApiError) {
        setError(err.message);
        setRequestId(err.requestId);
      } else {
        setError("网络错误，请重试。");
      }
    }
  }, []);

  useEffect(() => {
    void load(null, false);
  }, [load]);

  return (
    <div className="mx-auto max-w-3xl px-4 py-10">
      <header>
        <h2 className="font-display text-3xl font-bold">个人活动</h2>
        <p className="mt-1 font-body text-sm text-neutral-600">
          仅显示你自己的事件；记录不包含用户名、标题或任何敏感值。
        </p>
      </header>

      <div className="mt-6">
        <ErrorSummary message={error ?? ""} requestId={requestId} onDismiss={() => setError(null)} />
        {entries === null ? (
          <p className="font-body text-sm text-neutral-600">正在加载活动…</p>
        ) : entries.length === 0 ? (
          <p className="font-body text-sm text-neutral-600">暂无活动记录。</p>
        ) : (
          <ul className="divide-y divide-divider border-y border-divider">
            {entries.map((entry) => (
              <li key={entry.id} className="flex flex-wrap items-baseline justify-between gap-2 py-3">
                <div>
                  <p className="font-mono text-xs uppercase tracking-widest">
                    {eventLabels[entry.event] ?? entry.event}
                  </p>
                  {entry.target_type && entry.target_id && (
                    <p className="mt-1 font-mono text-xs text-neutral-500">
                      {entry.target_type} · {entry.target_id.slice(0, 8)}…
                    </p>
                  )}
                </div>
                <div className="text-right font-mono text-xs text-neutral-500">
                  <p className={entry.result === "failure" ? "text-accent" : ""}>
                    {entry.result === "success" ? "成功" : "失败"}
                  </p>
                  <p>{entry.created_at}</p>
                </div>
              </li>
            ))}
          </ul>
        )}
        {cursor && (
          <Button variant="secondary" className="mt-6" onClick={() => void load(cursor, true)}>
            加载更多
          </Button>
        )}
      </div>
    </div>
  );
}
