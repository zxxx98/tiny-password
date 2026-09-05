import { useCallback, useEffect, useState } from "react";
import { ApiError, fetchCsrfToken, getJSON, postJSON } from "../../app/api";

type SetupStatus = { initialized: boolean };

type Phase = "checking" | "ready" | "initialized" | "submitting" | "success";

const fieldClassName =
  "w-full min-h-[44px] border-b-2 border-ink bg-transparent px-3 py-2 font-mono text-sm focus-visible:bg-neutral-100 focus-visible:outline-none";

// SetupPage implements the one-time administrator initialization. The setup
// token is shown once in the container log (decision D01) and is required
// here; after success the server closes this endpoint permanently.
export function SetupPage() {
  const [phase, setPhase] = useState<Phase>("checking");
  const [token, setToken] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    getJSON<SetupStatus>("/api/v1/setup/status")
      .then((status) => {
        if (!cancelled) {
          setPhase(status.initialized ? "initialized" : "ready");
        }
      })
      .catch(() => {
        if (!cancelled) {
          setError("无法连接服务，请确认实例已启动。");
          setPhase("ready");
        }
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const submit = useCallback(
    async (event: React.FormEvent) => {
      event.preventDefault();
      setError(null);
      if (password !== confirm) {
        setError("两次输入的密码不一致。");
        return;
      }
      setPhase("submitting");
      try {
        const csrfToken = await fetchCsrfToken();
        await postJSON(
          "/api/v1/setup/init",
          { token: token, username: username, password: password },
          { csrfToken: csrfToken },
        );
        setToken("");
        setPassword("");
        setConfirm("");
        setPhase("success");
      } catch (err) {
        if (err instanceof ApiError) {
          switch (err.code) {
            case "SETUP_TOKEN_INVALID":
              setError("初始化令牌错误，请对照容器日志中的 setup_token_issued 事件。");
              break;
            case "SETUP_ALREADY_DONE":
              setPhase("initialized");
              return;
            case "RATE_LIMITED":
              setError("尝试过于频繁，请稍后再试。");
              break;
            default:
              setError(err.message);
          }
        } else {
          setError("网络错误，请重试。");
        }
        setPhase("ready");
      }
    },
    [token, username, password, confirm],
  );

  if (phase === "checking") {
    return <p className="font-body text-neutral-600">正在检查实例状态…</p>;
  }

  if (phase === "initialized") {
    return (
      <div className="border border-ink p-6">
        <h2 className="font-display text-2xl font-bold">本实例已完成初始化</h2>
        <p className="mt-3 font-body">
          初始化入口在实例创建管理员后永久关闭。请前往登录页使用现有账号。
        </p>
      </div>
    );
  }

  if (phase === "success") {
    return (
      <div className="border border-ink p-6" role="status">
        <h2 className="font-display text-2xl font-bold">管理员已创建</h2>
        <p className="mt-3 font-body">
          初始化完成，该入口已永久关闭。请使用管理员账号登录。
        </p>
        <a
          href="/login"
          className="mt-4 inline-block min-h-[44px] bg-ink px-6 py-3 text-xs font-semibold uppercase tracking-widest text-paper hover:bg-paper hover:text-ink hover:outline hover:outline-2 hover:outline-ink"
        >
          前往登录
        </a>
      </div>
    );
  }

  return (
    <form onSubmit={submit} noValidate>
      {error && (
        <p
          role="alert"
          className="mb-4 border-l-4 border-accent bg-neutral-100 px-4 py-3 font-body text-sm"
        >
          {error}
        </p>
      )}

      <div className="space-y-5">
        <div>
          <label htmlFor="setup-token" className="block font-mono text-xs uppercase tracking-widest">
            初始化令牌
          </label>
          <input
            id="setup-token"
            name="setup-token"
            type="password"
            autoComplete="off"
            required
            value={token}
            onChange={(e) => setToken(e.target.value)}
            className={fieldClassName}
          />
          <p className="mt-1 font-body text-xs text-neutral-500">
            一次性令牌在实例首次启动时输出到容器日志（setup_token_issued 事件）。
          </p>
        </div>

        <div>
          <label htmlFor="setup-username" className="block font-mono text-xs uppercase tracking-widest">
            管理员用户名
          </label>
          <input
            id="setup-username"
            name="setup-username"
            type="text"
            autoComplete="off"
            required
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            className={fieldClassName}
          />
        </div>

        <div>
          <label htmlFor="setup-password" className="block font-mono text-xs uppercase tracking-widest">
            管理员密码
          </label>
          <input
            id="setup-password"
            name="setup-password"
            type="password"
            autoComplete="new-password"
            required
            minLength={12}
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            className={fieldClassName}
          />
          <p className="mt-1 font-body text-xs text-neutral-500">至少 12 个字符。</p>
        </div>

        <div>
          <label htmlFor="setup-confirm" className="block font-mono text-xs uppercase tracking-widest">
            确认密码
          </label>
          <input
            id="setup-confirm"
            name="setup-confirm"
            type="password"
            autoComplete="new-password"
            required
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
            className={fieldClassName}
          />
        </div>
      </div>

      <button
        type="submit"
        disabled={phase === "submitting"}
        className="mt-6 min-h-[44px] w-full bg-ink px-6 py-3 text-xs font-semibold uppercase tracking-widest text-paper hover:bg-paper hover:text-ink hover:outline hover:outline-2 hover:outline-ink disabled:cursor-not-allowed disabled:opacity-50 md:w-auto"
      >
        {phase === "submitting" ? "正在创建…" : "创建管理员"}
      </button>
    </form>
  );
}
