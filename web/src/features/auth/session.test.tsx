import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LoginPage } from "./LoginPage";
import { ChangePasswordPage } from "./ChangePasswordPage";
import { SessionBoundary } from "./SessionBoundary";
import { SESSION_EXPIRED_EVENT, sessionStore } from "../../app/session";
import { request } from "../../app/api";

function jsonResponse(status: number, body: unknown, headers?: Record<string, string>) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json", ...headers },
  });
}

type Stub = { status: number; body?: unknown; headers?: Record<string, string> };

function stubFetch(steps: Stub[]) {
  const queue = [...steps];
  return vi.fn(async (_url: string | URL, init?: RequestInit): Promise<Response> => {
    void init;
    const step = queue.shift() ?? { status: 500, body: { code: "INTERNAL", message: "x", request_id: "r" } };
    if (step.status === 204) {
      return new Response(null, { status: 204 });
    }
    return jsonResponse(step.status, step.body ?? {}, step.headers);
  });
}

afterEach(() => {
  vi.unstubAllGlobals();
  sessionStore.clear();
  window.history.replaceState({}, "", "/");
});

describe("LoginPage", () => {
  it("performs the pre-auth CSRF handshake then logs in and stores the session in memory", async () => {
    const user = userEvent.setup();
    const fetchMock = stubFetch([
      { status: 200, body: { csrf_token: "pre-auth" }, headers: { "Set-Cookie": "pre_auth=x" } },
      {
        status: 200,
        body: {
          must_change_password: false,
          csrf_token: "session-csrf",
          user: {
            user_id: "u1",
            username: "alice",
            role: "member",
            must_change_password: false,
            idle_timeout_minutes: 15,
          },
        },
      },
    ]);
    vi.stubGlobal("fetch", fetchMock);
    const onDone = vi.fn();
    render(<LoginPage onDone={onDone} />);
    await user.type(screen.getByLabelText("用户名"), "alice");
    await user.type(screen.getByLabelText("密码"), "password-123456");
    await user.click(screen.getByRole("button", { name: "登录" }));

    await waitFor(() => expect(onDone).toHaveBeenCalledTimes(1));
    expect(fetchMock).toHaveBeenCalledTimes(2);
    const loginCall = fetchMock.mock.calls[1];
    expect(loginCall[0]).toBe("/api/v1/auth/login");
    expect((loginCall[1] as RequestInit).headers).toMatchObject({ "X-CSRF-Token": "pre-auth" });
    expect(sessionStore.get().csrfToken).toBe("session-csrf");
    expect(sessionStore.get().principal?.username).toBe("alice");
  });

  it("shows one uniform message for wrong credentials without enumeration", async () => {
    const user = userEvent.setup();
    const fetchMock = stubFetch([
      { status: 200, body: { csrf_token: "pre-auth" } },
      { status: 401, body: { code: "UNAUTHORIZED", message: "invalid credentials or session", request_id: "r-1" } },
    ]);
    vi.stubGlobal("fetch", fetchMock);
    render(<LoginPage />);
    await user.type(screen.getByLabelText("用户名"), "alice");
    await user.type(screen.getByLabelText("密码"), "wrong-password!");
    await user.click(screen.getByRole("button", { name: "登录" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("用户名或密码错误。");
    expect(alert).toHaveTextContent("request_id: r-1");
  });
});

describe("SessionBoundary", () => {
  it("shows the login page without a session", () => {
    render(
      <SessionBoundary>
        <p>受保护内容</p>
      </SessionBoundary>,
    );
    expect(screen.getByRole("heading", { name: "登录" })).toBeInTheDocument();
    expect(screen.queryByText("受保护内容")).not.toBeInTheDocument();
  });

  it("pins a first-login principal to the password rotation and releases after the change", async () => {
    const user = userEvent.setup();
    sessionStore.set({
      principal: {
        user_id: "u1",
        username: "newbie",
        role: "member",
        must_change_password: true,
        idle_timeout_minutes: 15,
      },
      csrfToken: "t",
    });
    const fetchMock = stubFetch([
      { status: 204, headers: { "X-CSRF-Token": "rotated" } },
      {
        status: 200,
        body: {
          user: {
            user_id: "u1",
            username: "newbie",
            role: "member",
            must_change_password: false,
            idle_timeout_minutes: 15,
          },
          csrf_token: "session-csrf-2",
        },
      },
    ]);
    vi.stubGlobal("fetch", fetchMock);
    render(
      <SessionBoundary>
        <p>受保护内容</p>
      </SessionBoundary>,
    );
    expect(screen.queryByText("受保护内容")).not.toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "设置新密码" })).toBeInTheDocument();

    await user.type(screen.getByLabelText("当前密码"), "initial-password-1");
    await user.type(screen.getByLabelText("新密码"), "brand-new-password-1");
    await user.type(screen.getByLabelText("确认新密码"), "brand-new-password-1");
    await user.click(screen.getByRole("button", { name: "设置并继续" }));

    await waitFor(() => expect(screen.getByText("受保护内容")).toBeInTheDocument());
    // The authoritative principal came from /auth/session and the rotated
    // CSRF token replaced the in-memory one.
    expect(fetchMock.mock.calls[1][0]).toBe("/api/v1/auth/session");
    expect(sessionStore.get().principal?.must_change_password).toBe(false);
    expect(sessionStore.get().csrfToken).toBe("session-csrf-2");
  });

  it("aborts in-flight requests when the session expires, then shows login", async () => {
    sessionStore.set({
      principal: {
        user_id: "u1",
        username: "alice",
        role: "member",
        must_change_password: false,
        idle_timeout_minutes: 15,
      },
      csrfToken: "t",
    });
    const neverSettles = vi.fn(
      (_url: string | URL, init?: RequestInit) =>
        new Promise<Response>((_resolve, reject) => {
          init?.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
        }),
    );
    vi.stubGlobal("fetch", neverSettles);
    render(
      <SessionBoundary>
        <p>受保护内容</p>
      </SessionBoundary>,
    );
    request("GET", "/api/v1/items").catch(() => undefined); // in-flight vault request
    // Exactly what the API client does on a 401: clear, then broadcast.
    sessionStore.clear();
    window.dispatchEvent(new CustomEvent(SESSION_EXPIRED_EVENT));
    await waitFor(() => expect(screen.getByRole("heading", { name: "登录" })).toBeInTheDocument());
    // The in-flight request was aborted rather than left running.
    expect(neverSettles.mock.calls[0][1]?.signal?.aborted).toBe(true);
  });
});

describe("ChangePasswordPage", () => {
  it("rejects mismatched confirmation without a network call", async () => {
    const user = userEvent.setup();
    const fetchMock = stubFetch([]);
    vi.stubGlobal("fetch", fetchMock);
    render(<ChangePasswordPage />);
    await user.type(screen.getByLabelText("当前密码"), "current-password-1");
    await user.type(screen.getByLabelText("新密码"), "brand-new-password-1");
    await user.type(screen.getByLabelText("确认新密码"), "different-password!");
    await user.click(screen.getByRole("button", { name: "修改密码" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("两次输入的新密码不一致。");
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
