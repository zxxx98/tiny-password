import { useCallback, useEffect, useState } from "react";
import { ApiError, request } from "../../app/api";
import { navigate } from "../../app/router";
import { sessionStore, useSession } from "../../app/session";
import { Button } from "../../design-system/Button";
import { ConfirmDialog } from "../../design-system/Dialog";
import { ErrorSummary, StatusBanner } from "../../design-system/Status";
import { ChangePasswordPage } from "./ChangePasswordPage";

type SessionEntry = {
  id: string;
  created_at: string;
  expires_at: string;
  idle_expires_at?: string;
  current?: boolean;
};

const errorMessages: Record<string, string> = {
  UNAUTHORIZED: "会话已失效，请重新登录。",
  FORBIDDEN: "没有执行该操作的权限。",
};

/**
 * AccountPage: profile, self-service password change, session list with
 * revocation, idle-timeout preference (5–30 minutes) and logout. Every
 * action leaves data in memory only.
 */
export function AccountPage() {
  const { principal, csrfToken } = useSession();
  const [sessions, setSessions] = useState<SessionEntry[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [requestId, setRequestId] = useState<string | undefined>();
  const [banner, setBanner] = useState<string | null>(null);
  const [revoking, setRevoking] = useState<string | null>(null);
  const [confirmRevoke, setConfirmRevoke] = useState<SessionEntry | null>(null);
  const [confirmLogout, setConfirmLogout] = useState(false);
  const [showChange, setShowChange] = useState(false);

  const fail = useCallback((err: unknown) => {
    if (err instanceof ApiError) {
      setError(errorMessages[err.code] ?? err.message);
      setRequestId(err.requestId);
    } else {
      setError("网络错误，请重试。");
    }
  }, []);

  const loadSessions = useCallback(async () => {
    try {
      const page = await request<{ items: SessionEntry[] }>(
        "GET",
        "/api/v1/auth/sessions",
        undefined,
        { csrfToken },
      );
      setSessions(page.items);
      setError(null);
    } catch (err) {
      fail(err);
    }
  }, [csrfToken, fail]);

  useEffect(() => {
    void loadSessions();
  }, [loadSessions]);

  const revoke = async (entry: SessionEntry) => {
    setRevoking(entry.id);
    try {
      await request("DELETE", `/api/v1/auth/sessions/${entry.id}`, undefined, { csrfToken });
      setBanner(entry.current ? "当前会话已退出。" : "该会话已撤销。");
      if (entry.current) {
        sessionStore.clear();
        navigate("/");
        return;
      }
      await loadSessions();
    } catch (err) {
      fail(err);
    } finally {
      setRevoking(null);
      setConfirmRevoke(null);
    }
  };

  const logout = async () => {
    try {
      await request("POST", "/api/v1/auth/logout", undefined, { csrfToken });
    } catch {
      // The server session may already be gone; clear locally regardless.
    }
    sessionStore.clear();
    navigate("/");
  };

  const saveIdleTimeout = async (minutes: number) => {
    try {
      await request("PATCH", "/api/v1/auth/preferences", { idle_timeout_minutes: minutes }, { csrfToken });
      setBanner(`闲置自动锁定已调整为 ${minutes} 分钟。`);
      const state = sessionStore.get();
      if (state.principal) {
        sessionStore.set({ ...state, principal: { ...state.principal, idle_timeout_minutes: minutes } });
      }
    } catch (err) {
      fail(err);
    }
  };

  if (!principal) {
    return null;
  }

  return (
    <div className="mx-auto max-w-3xl space-y-10 px-4 py-10">
      <header>
        <h2 className="font-display text-3xl font-bold">账户</h2>
        <p className="mt-1 font-body text-sm text-neutral-600">
          以 <span className="font-mono">{principal.username}</span> 登录 · 角色{" "}
          <span className="font-mono">{principal.role === "admin" ? "管理员" : "成员"}</span>
        </p>
      </header>

      <ErrorSummary message={error ?? ""} requestId={requestId} onDismiss={() => setError(null)} />
      {banner && (
        <StatusBanner>
          {banner}{" "}
          <button
            type="button"
            aria-label="关闭状态提示"
            className="font-mono text-xs uppercase tracking-widest underline-offset-4 hover:underline"
            onClick={() => setBanner(null)}
          >
            知道了
          </button>
        </StatusBanner>
      )}

      <section aria-labelledby="account-security-heading" className="border border-ink p-6">
        <h3 id="account-security-heading" className="font-display text-xl font-bold">
          安全
        </h3>
        <p className="mt-1 font-body text-sm text-neutral-600">
          修改密码会撤销其他所有会话。
        </p>
        {!showChange ? (
          <Button variant="secondary" className="mt-4" onClick={() => setShowChange(true)}>
            修改密码
          </Button>
        ) : (
          <div className="mt-4">
            <ChangePasswordPage
              onDone={() => {
                setShowChange(false);
                setBanner("密码已修改，其他会话已撤销。");
                void loadSessions();
              }}
            />
          </div>
        )}
      </section>

      <section aria-labelledby="account-sessions-heading" className="border border-ink p-6">
        <h3 id="account-sessions-heading" className="font-display text-xl font-bold">
          会话
        </h3>
        {sessions === null ? (
          <p className="mt-2 font-body text-sm text-neutral-600">正在加载会话…</p>
        ) : (
          <ul className="mt-4 divide-y divide-divider border-y border-divider">
            {sessions.map((entry) => (
              <li key={entry.id} className="flex flex-wrap items-center justify-between gap-3 py-3">
                <div className="font-mono text-xs">
                  <p>
                    会话 <span className="text-neutral-500">{entry.id.slice(0, 8)}…</span>
                    {entry.current && (
                      <span className="ml-2 border border-ink px-1 py-0.5 uppercase">当前</span>
                    )}
                  </p>
                  <p className="mt-1 text-neutral-500">过期：{entry.expires_at}</p>
                </div>
                <Button
                  variant="ghost"
                  disabled={revoking === entry.id}
                  onClick={() => setConfirmRevoke(entry)}
                >
                  {revoking === entry.id ? "撤销中…" : "撤销"}
                </Button>
              </li>
            ))}
          </ul>
        )}
        <ConfirmDialog
          open={confirmRevoke !== null}
          danger
          title="撤销该会话？"
          description="该设备的登录状态会立即失效。"
          confirmLabel="撤销"
          onCancel={() => setConfirmRevoke(null)}
          onConfirm={() => {
            if (confirmRevoke) void revoke(confirmRevoke);
          }}
        />
      </section>

      <section aria-labelledby="account-preferences-heading" className="border border-ink p-6">
        <h3 id="account-preferences-heading" className="font-display text-xl font-bold">
          闲置自动锁定
        </h3>
        <p className="mt-1 font-body text-sm text-neutral-600">无操作超过该时长后需要重新登录。</p>
        <div className="mt-4 flex flex-wrap gap-2">
          {[5, 10, 15, 20, 30].map((minutes) => (
            <Button
              key={minutes}
              variant={principal.idle_timeout_minutes === minutes ? "primary" : "secondary"}
              onClick={() => void saveIdleTimeout(minutes)}
            >
              {minutes} 分钟
            </Button>
          ))}
        </div>
      </section>

      <section aria-labelledby="account-activity-link-heading" className="border border-ink p-6">
        <h3 id="account-activity-link-heading" className="font-display text-xl font-bold">
          个人活动
        </h3>
        <p className="mt-1 font-body text-sm text-neutral-600">查看与你相关的脱敏审计事件。</p>
        <Button variant="secondary" className="mt-4" onClick={() => navigate("/account/activity")}>
          查看个人活动
        </Button>
      </section>

      <section className="border border-ink p-6">
        <Button variant="secondary" onClick={() => setConfirmLogout(true)}>
          退出登录
        </Button>
        <ConfirmDialog
          open={confirmLogout}
          title="退出登录？"
          description="本地内存中的会话与数据都会被清空。"
          confirmLabel="退出"
          onCancel={() => setConfirmLogout(false)}
          onConfirm={() => void logout()}
        />
      </section>
    </div>
  );
}
