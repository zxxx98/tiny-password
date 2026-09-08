import { useCallback, useState } from "react";
import { ApiError, fetchCsrfToken, request } from "../../app/api";
import { sessionStore, type Principal } from "../../app/session";
import { Button } from "../../design-system/Button";
import { Field } from "../../design-system/Field";
import { ErrorSummary } from "../../design-system/Status";
import { VaultIllustration } from "./VaultIllustration";

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
    <div className="grid min-h-[calc(100svh-13rem)] items-center gap-12 px-6 py-12 md:min-h-[calc(100svh-8rem)] md:grid-cols-12 md:px-10 md:py-16 lg:gap-20 lg:px-16">
    <form onSubmit={submit} noValidate className="mx-auto w-full max-w-md md:col-span-5" aria-labelledby="login-heading">
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
    <figure className="hidden border-l border-divider pl-10 md:col-span-7 md:block lg:pl-16">
      <VaultIllustration />
      <figcaption className="mt-6 flex items-center gap-4 border-t border-divider pt-4 font-mono text-xs text-neutral-500">
        <span className="text-accent">FIG. 01</span>
        <span>你的密码，有序珍藏。</span>
      </figcaption>
    </figure>
    </div>
  );
}
