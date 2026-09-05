import { useCallback, useEffect, useState } from "react";
import { ApiError, request } from "../../app/api";
import { useSession } from "../../app/session";
import { Button } from "../../design-system/Button";
import { ConfirmDialog } from "../../design-system/Dialog";
import { Field } from "../../design-system/Field";
import { ErrorSummary, StatusBanner } from "../../design-system/Status";

type User = {
  id: string;
  username: string;
  role: "admin" | "member";
  status: "active" | "disabled" | "must_change_password";
  created_at: string;
};

type Page = { items: User[]; next_cursor: string | null };

const statusLabels: Record<User["status"], string> = {
  active: "启用",
  disabled: "已停用",
  must_change_password: "待首登改密",
};

const errorMessages: Record<string, string> = {
  USERNAME_TAKEN: "该用户名已被占用。",
  VALIDATION_ERROR: "输入不符合要求（用户名 3–64 字符；初始密码至少 12 字符）。",
  LAST_ADMIN_PROTECTED: "最后一位管理员不能被停用或删除。",
  CONFIRMATION_MISMATCH: "确认用户名与目标成员不一致。",
  FORBIDDEN: "需要管理员角色。",
};

const initialPassword = () => {
  // A readable, high-entropy initial password composed locally; it is shown
  // once to the admin and never stored by the page.
  const alphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789";
  const bytes = new Uint8Array(16);
  crypto.getRandomValues(bytes);
  let out = "";
  for (const byte of bytes) {
    out += alphabet[byte % alphabet.length];
  }
  return out.slice(0, 8) + "-" + out.slice(8);
};

/**
 * UsersPage: the admin member-management console. Deletion is a deliberate
 * three-step flow — export reminder, shared-data impact statement, then an
 * exact username confirmation — while the server independently re-validates.
 */
export function UsersPage() {
  const { csrfToken } = useSession();
  const [users, setUsers] = useState<User[] | null>(null);
  const [cursor, setCursor] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [requestId, setRequestId] = useState<string | undefined>();
  const [banner, setBanner] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  const [creating, setCreating] = useState(false);
  const [newUsername, setNewUsername] = useState("");
  const [newPassword, setNewPassword] = useState(initialPassword);
  const [issuedPassword, setIssuedPassword] = useState<string | null>(null);

  const [deleteTarget, setDeleteTarget] = useState<User | null>(null);
  const [confirmText, setConfirmText] = useState("");
  const [step, setStep] = useState<"warn" | "confirm">("warn");

  const fail = useCallback((err: unknown) => {
    if (err instanceof ApiError) {
      setError(errorMessages[err.code] ?? err.message);
      setRequestId(err.requestId);
    } else {
      setError("网络错误，请重试。");
    }
  }, []);

  const load = useCallback(
    async (nextCursor: string | null, append: boolean) => {
      try {
        const path = nextCursor ? `/api/v1/users?cursor=${encodeURIComponent(nextCursor)}` : "/api/v1/users";
        const page = await request<Page>("GET", path, undefined, { csrfToken });
        setUsers((prev) => (append && prev ? [...prev, ...page.items] : page.items));
        setCursor(page.next_cursor);
        setError(null);
      } catch (err) {
        fail(err);
      }
    },
    [csrfToken, fail],
  );

  useEffect(() => {
    void load(null, false);
  }, [load]);

  const create = async (event: React.FormEvent) => {
    event.preventDefault();
    setBusy("create");
    setIssuedPassword(null);
    try {
      const created = await request<User>(
        "POST",
        "/api/v1/users",
        { username: newUsername, initial_password: newPassword },
        { csrfToken },
      );
      setIssuedPassword(newPassword);
      setBanner(`成员 ${created.username} 已创建（${created.id.slice(0, 8)}…）。`);
      setNewUsername("");
      setNewPassword(initialPassword());
      setCreating(false);
      await load(null, false);
    } catch (err) {
      fail(err);
    } finally {
      setBusy(null);
    }
  };

  const act = async (user: User, action: "disable" | "enable" | "revoke-sessions", label: string) => {
    setBusy(user.id + action);
    try {
      await request("POST", `/api/v1/users/${user.id}/${action}`, undefined, { csrfToken });
      setBanner(`${user.username}：${label}。`);
      await load(null, false);
    } catch (err) {
      fail(err);
    } finally {
      setBusy(null);
    }
  };

  const remove = async () => {
    if (!deleteTarget) {
      return;
    }
    setBusy(deleteTarget.id + "delete");
    try {
      await request("DELETE", `/api/v1/users/${deleteTarget.id}`, { confirm_username: confirmText }, { csrfToken });
      setBanner(`成员 ${deleteTarget.username} 已永久删除，关联数据已清除。`);
      setDeleteTarget(null);
      setConfirmText("");
      setStep("warn");
      await load(null, false);
    } catch (err) {
      fail(err);
      setStep("warn");
    } finally {
      setBusy(null);
    }
  };

  return (
    <div className="mx-auto max-w-3xl px-4 py-10">
      <header className="flex flex-wrap items-center justify-between gap-4">
        <div>
          <h2 className="font-display text-3xl font-bold">成员管理</h2>
          <p className="mt-1 font-body text-sm text-neutral-600">
            创建、停用与删除实例成员；操作在服务端再次校验权限。
          </p>
        </div>
        <Button onClick={() => setCreating((prev) => !prev)}>{creating ? "收起" : "创建成员"}</Button>
      </header>

      <div className="mt-6">
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
        {issuedPassword && (
          <div role="alert" className="mb-4 border-l-4 border-accent bg-neutral-100 px-4 py-3">
            <p className="font-body text-sm">请立即复制初始密码，它只显示这一次：</p>
            <p className="mt-1 select-all font-mono text-sm">{issuedPassword}</p>
          </div>
        )}
      </div>

      {creating && (
        <form onSubmit={create} noValidate className="mb-8 space-y-4 border border-ink p-6">
          <Field
            id="new-username"
            label="用户名"
            autoComplete="off"
            required
            value={newUsername}
            onChange={(e) => setNewUsername(e.target.value)}
            hint="3–64 个字符，可含字母、数字、点、下划线、连字符。"
          />
          <Field
            id="new-password"
            label="初始密码"
            required
            value={newPassword}
            onChange={(e) => setNewPassword(e.target.value)}
            hint="成员首次登录必须更换。"
          />
          <Button type="submit" disabled={busy === "create"}>
            {busy === "create" ? "创建中…" : "创建成员"}
          </Button>
        </form>
      )}

      {users === null ? (
        <p className="font-body text-sm text-neutral-600">正在加载成员…</p>
      ) : (
        <ul className="divide-y divide-divider border-y border-divider">
          {users.map((user) => (
            <li key={user.id} className="flex flex-wrap items-center justify-between gap-3 py-4">
              <div>
                <p className="font-body font-semibold">
                  {user.username}
                  {user.role === "admin" && (
                    <span className="ml-2 border border-ink px-1 py-0.5 font-mono text-xs uppercase">管理员</span>
                  )}
                  <span className="ml-2 font-mono text-xs text-neutral-500">{statusLabels[user.status]}</span>
                </p>
                <p className="mt-1 font-mono text-xs text-neutral-500">
                  ID {user.id.slice(0, 8)}… · 创建于 {user.created_at.slice(0, 10)}
                </p>
              </div>
              <div className="flex flex-wrap gap-2">
                {user.status === "disabled" ? (
                  <Button
                    variant="secondary"
                    disabled={busy === user.id + "enable"}
                    onClick={() => void act(user, "enable", "已启用")}
                  >
                    启用
                  </Button>
                ) : (
                  <Button
                    variant="secondary"
                    disabled={busy === user.id + "disable"}
                    onClick={() => void act(user, "disable", "已停用，会话已撤销")}
                  >
                    停用
                  </Button>
                )}
                <Button
                  variant="ghost"
                  disabled={busy === user.id + "revoke-sessions"}
                  onClick={() => void act(user, "revoke-sessions", "全部会话已撤销")}
                >
                  撤销会话
                </Button>
                <Button variant="ghost" onClick={() => { setDeleteTarget(user); setConfirmText(""); setStep("warn"); }}>
                  删除
                </Button>
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

      <ConfirmDialog
        open={deleteTarget !== null && step === "warn"}
        danger
        title={`删除成员 ${deleteTarget?.username ?? ""}？`}
        description={
          <>
            <p className="font-semibold text-ink">删除前请先确认已完成数据导出。</p>
            <p className="mt-2">
              该成员的个人条目、其创建的共享条目及其历史版本将被永久清除，无法通过产品恢复；审计记录仅保留不透明 ID。
            </p>
          </>
        }
        confirmLabel="继续删除"
        onCancel={() => setDeleteTarget(null)}
        onConfirm={() => setStep("confirm")}
      />
      <ConfirmDialog
        open={deleteTarget !== null && step === "confirm"}
        danger
        title="输入成员用户名以确认"
        description={
          <div className="space-y-3">
            <p>请精确输入目标成员的用户名（区分大小写）以继续。服务端将独立校验。</p>
            <Field
              id="delete-confirm-username"
              label="目标用户名"
              autoComplete="off"
              value={confirmText}
              onChange={(e) => setConfirmText(e.target.value)}
            />
          </div>
        }
        confirmLabel={busy === deleteTarget?.id + "delete" ? "删除中…" : "永久删除"}
        cancelLabel="取消"
        onCancel={() => { setDeleteTarget(null); setStep("warn"); }}
        onConfirm={() => void remove()}
      />
    </div>
  );
}
