import { useCallback, useEffect, type ReactNode } from "react";
import { abortInFlightRequests, request } from "../../app/api";
import { navigate } from "../../app/router";
import { SESSION_EXPIRED_EVENT, sessionStore, useSession, type Principal } from "../../app/session";
import { ChangePasswordPage } from "./ChangePasswordPage";
import { LoginPage } from "./LoginPage";

/**
 * SessionBoundary gates every protected route:
 *  - without a principal it renders the login page;
 *  - a first-login principal is pinned to the password rotation flow and
 *    cannot reach the vault before completing it;
 *  - a 401 anywhere aborts every in-flight request and clears the in-memory
 *    session so late responses can never repopulate vault, search, form or
 *    generator state — this component then swaps to the login view.
 */
export function SessionBoundary({ children, requireAdmin }: { children: ReactNode; requireAdmin?: boolean }) {
  const { principal } = useSession();

  useEffect(() => {
    const onExpired = () => {
      abortInFlightRequests();
    };
    window.addEventListener(SESSION_EXPIRED_EVENT, onExpired);
    return () => window.removeEventListener(SESSION_EXPIRED_EVENT, onExpired);
  }, []);

  // After the forced rotation the server is the only authority on the flag.
  const refreshAfterRotation = useCallback(() => {
    void (async () => {
      try {
        const result = await request<{ user: Principal; csrf_token: string }>(
          "GET",
          "/api/v1/auth/session",
          undefined,
          { csrfToken: sessionStore.get().csrfToken },
        );
        sessionStore.set({ principal: result.user, csrfToken: result.csrf_token });
        if (!result.user.must_change_password) {
          navigate("/vault");
        }
      } catch {
        // The next guarded request will surface the 401 and clear the session.
      }
    })();
  }, []);

  if (!principal) {
    return <LoginPage onDone={() => navigate("/vault")} />;
  }
  if (principal.must_change_password) {
    return <ChangePasswordPage forced onDone={refreshAfterRotation} />;
  }
  if (requireAdmin && principal.role !== "admin") {
    return (
      <div className="mx-auto max-w-screen-xl px-4 py-16" role="alert">
        <h2 className="font-display text-2xl font-bold">需要管理员角色</h2>
        <p className="mt-3 font-body text-sm text-neutral-600">
          该页面仅对管理员开放。服务端同样会拒绝未授权请求。
        </p>
      </div>
    );
  }
  return <>{children}</>;
}
