import { useCallback, useState } from "react";
import { ApiError, request } from "../../app/api";
import { sessionStore } from "../../app/session";
import { Button } from "../../design-system/Button";
import { Field } from "../../design-system/Field";
import { ErrorSummary } from "../../design-system/Status";

const errorMessages: Record<string, string> = {
  UNAUTHORIZED: "当前密码不正确。",
  VALIDATION_ERROR: "新密码不满足要求：至少 12 个字符。",
  RATE_LIMITED: "尝试过于频繁，请稍后再试。",
};

/**
 * ChangePasswordPage serves both the forced first-login rotation and the
 * account page's self-service change. A success rotates the session cookie;
 * the server returns the new CSRF token in a response header, which replaces
 * the in-memory one. All old sessions stay revoked server-side.
 */
export function ChangePasswordPage({
  forced,
  onDone,
}: {
  /** True during the first-login flow: the change cannot be skipped. */
  forced?: boolean;
  onDone?: () => void;
}) {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [requestId, setRequestId] = useState<string | undefined>();
  const [submitting, setSubmitting] = useState(false);

  const submit = useCallback(
    async (event: React.FormEvent) => {
      event.preventDefault();
      setError(null);
      setRequestId(undefined);
      if (next !== confirm) {
        setError("两次输入的新密码不一致。");
        return;
      }
      setSubmitting(true);
      let rotatedCsrf: string | undefined;
      try {
        await request(
          "POST",
          "/api/v1/auth/password",
          { current_password: current, new_password: next },
          {
            csrfToken: sessionStore.get().csrfToken,
            onResponse: (response) => {
              rotatedCsrf = response.headers.get("X-CSRF-Token") ?? undefined;
            },
          },
        );
        setCurrent("");
        setNext("");
        setConfirm("");
        const state = sessionStore.get();
        sessionStore.set({ ...state, csrfToken: rotatedCsrf ?? state.csrfToken });
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
    [current, next, confirm, onDone],
  );

  return (
    <form onSubmit={submit} noValidate className="max-w-md" aria-labelledby="change-password-heading">
      <h2 id="change-password-heading" className="font-display text-2xl font-bold">
        {forced ? "设置新密码" : "修改密码"}
      </h2>
      <p className="mb-6 mt-2 font-body text-sm text-neutral-600">
        {forced
          ? "首次登录必须设置新密码后才能进入保险库。修改完成后，所有旧会话将被撤销。"
          : "修改完成后，其他所有会话将被撤销。"}
      </p>
      <ErrorSummary message={error ?? ""} requestId={requestId} />
      <div className="space-y-5">
        <Field
          id="change-current"
          label="当前密码"
          type="password"
          autoComplete="current-password"
          required
          value={current}
          onChange={(e) => setCurrent(e.target.value)}
        />
        <Field
          id="change-new"
          label="新密码"
          type="password"
          autoComplete="new-password"
          minLength={12}
          required
          value={next}
          onChange={(e) => setNext(e.target.value)}
          hint="至少 12 个字符。"
        />
        <Field
          id="change-confirm"
          label="确认新密码"
          type="password"
          autoComplete="new-password"
          required
          value={confirm}
          onChange={(e) => setConfirm(e.target.value)}
        />
      </div>
      <Button type="submit" disabled={submitting} className="mt-6 w-full">
        {submitting ? "正在修改…" : forced ? "设置并继续" : "修改密码"}
      </Button>
    </form>
  );
}
