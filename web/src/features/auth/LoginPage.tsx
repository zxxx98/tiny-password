import { useCallback, useState } from "react";
import { ApiError, fetchCsrfToken, request } from "../../app/api";
import { sessionStore, type Principal } from "../../app/session";
import { Button } from "../../design-system/Button";
import { Field } from "../../design-system/Field";
import { ErrorSummary } from "../../design-system/Status";

type LoginResponse = {
  must_change_password: boolean;
  csrf_token: string;
  user: Principal;
};

const errorMessages: Record<string, string> = {
  UNAUTHORIZED: "用户名或密码错误。",
  ACCOUNT_DISABLED: "该账户已被停用，请联系管理员。",
  RATE_LIMITED: "尝试过于频繁，请稍后再试。",
};

/**
 * LoginPage performs the pre-auth CSRF handshake, then the login. The
 * returned principal and session-bound CSRF token live in memory only.
 */
export function LoginPage({ onDone }: { onDone?: () => void }) {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [requestId, setRequestId] = useState<string | undefined>();
  const [submitting, setSubmitting] = useState(false);

  const submit = useCallback(
    async (event: React.FormEvent) => {
      event.preventDefault();
      setError(null);
      setRequestId(undefined);
      setSubmitting(true);
      try {
        const csrfToken = await fetchCsrfToken();
        const result = await request<LoginResponse>(
          "POST",
          "/api/v1/auth/login",
          { username, password },
          { csrfToken },
        );
        setPassword("");
        sessionStore.set({ principal: result.user, csrfToken: result.csrf_token });
        onDone?.();
      } catch (err) {
        if (err instanceof ApiError) {
          setError(errorMessages[err.code] ?? err.message);
          setRequestId(err.requestId);
        } else {
          setError("网络错误，请重试。");
        }
      } finally {
        setSubmitting(false);
      }
    },
    [username, password, onDone],
  );

  return (
    <form onSubmit={submit} noValidate className="max-w-md" aria-labelledby="login-heading">
      <h2 id="login-heading" className="font-display text-2xl font-bold">
        登录
      </h2>
      <p className="mb-6 mt-2 font-body text-sm text-neutral-600">
        使用实例成员账号登录 Tiny Password。
      </p>
      <ErrorSummary message={error ?? ""} requestId={requestId} />
      <div className="space-y-5">
        <Field
          id="login-username"
          label="用户名"
          autoComplete="username"
          required
          value={username}
          onChange={(e) => setUsername(e.target.value)}
        />
        <Field
          id="login-password"
          label="密码"
          type="password"
          autoComplete="current-password"
          required
          value={password}
          onChange={(e) => setPassword(e.target.value)}
        />
      </div>
      <Button type="submit" disabled={submitting} className="mt-6 w-full">
        {submitting ? "正在登录…" : "登录"}
      </Button>
    </form>
  );
}
