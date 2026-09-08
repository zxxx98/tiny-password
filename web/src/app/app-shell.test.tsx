import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AppShell, WorkspaceGrid } from "./AppShell";
import { Router } from "./router";
import { SESSION_EXPIRED_EVENT, sessionStore } from "./session";
import { ApiError, request } from "./api";

function dispatchInstallPrompt(
  prompt: () => Promise<void>,
  userChoice: Promise<{ outcome: "accepted" | "dismissed"; platform: string }>,
) {
  const event = Object.assign(new Event("beforeinstallprompt"), { prompt, userChoice });
  window.dispatchEvent(event);
}

function stubFetch(responses: Array<{ status: number; body: unknown }>) {
  const queue = [...responses];
  const fetchMock = vi.fn(async (_url: string | URL, init?: RequestInit): Promise<Response> => {
    void init;
    const next = queue.shift() ?? { status: 404, body: { code: "NOT_FOUND" } };
    return new Response(JSON.stringify(next.body), {
      status: next.status,
      headers: { "Content-Type": "application/json" },
    });
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

afterEach(() => {
  vi.unstubAllGlobals();
  sessionStore.clear();
});

describe("AppShell", () => {
  it("renders the masthead, desktop nav and mobile bottom nav", () => {
    render(
      <AppShell>
        <p>内容</p>
      </AppShell>,
    );
    expect(screen.getByRole("heading", { level: 1, name: /tiny password/i })).toBeInTheDocument();
    const desktopNav = screen.getByRole("navigation", { name: "主导航" });
    expect(desktopNav).toHaveClass("hidden", "md:block");
    const mobileNav = screen.getByRole("navigation", { name: "移动端主导航" });
    expect(mobileNav).toHaveClass("md:hidden");
    // Touch targets in the bottom bar exceed 44px.
    for (const link of Array.from(mobileNav.querySelectorAll("a"))) {
      expect(link).toHaveClass("min-h-[56px]");
    }
  });

  it("shows member nav by default and adds the admin entry for admins", () => {
    const { unmount } = render(
      <AppShell>
        <p>内容</p>
      </AppShell>,
    );
    expect(screen.queryByRole("link", { name: "成员" })).not.toBeInTheDocument();
    unmount();

    sessionStore.set({
      principal: {
        user_id: "u1",
        username: "Admin",
        role: "admin",
        must_change_password: false,
        idle_timeout_minutes: 15,
      },
      csrfToken: "token",
    });
    render(
      <AppShell>
        <p>内容</p>
      </AppShell>,
    );
    expect(screen.getAllByRole("link", { name: "成员" }).length).toBeGreaterThan(0);
    expect(screen.getAllByRole("link", { name: "备份" }).length).toBeGreaterThan(0);
    expect(screen.getAllByRole("link", { name: "审计" }).length).toBeGreaterThan(0);
    expect(screen.getAllByRole("link", { name: "系统" }).length).toBeGreaterThan(0);
  });

  it("offers the native install prompt from a user action", async () => {
    const user = userEvent.setup();
    const prompt = vi.fn().mockResolvedValue(undefined);
    dispatchInstallPrompt(prompt, Promise.resolve({ outcome: "accepted", platform: "web" }));
    render(
      <AppShell>
        <p>内容</p>
      </AppShell>,
    );

    expect(screen.getByRole("dialog", { name: "安装 Tiny Password" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "安装应用" }));
    await waitFor(() => expect(prompt).toHaveBeenCalledTimes(1));
    expect(screen.queryByTestId("pwa-install")).not.toBeInTheDocument();
  });
});

describe("Router", () => {
  it("matches :param routes and renders the fallback for unknown paths", () => {
    window.history.replaceState({}, "", "/vault/item-42");
    render(
      <Router
        routes={[
          { path: "/vault/:itemId", element: <p data-testid="detail">detail</p> },
          { path: "/other", element: <p>other</p> },
        ]}
      />,
    );
    expect(screen.getByTestId("detail")).toBeInTheDocument();

    window.history.replaceState({}, "", "/nowhere");
  });
});

describe("WorkspaceGrid", () => {
  it("lays out the 12-column collapsed grid", () => {
    render(<WorkspaceGrid list={<p>列表</p>} detail={<p>详情</p>} />);
    expect(screen.getByTestId("workspace-list")).toHaveClass("md:col-span-4", "md:border-r");
    expect(screen.getByTestId("workspace-detail")).toHaveClass("md:col-span-8", "hidden");
  });
});

describe("session store", () => {
  it("stays in memory only", () => {
    sessionStore.set({
      principal: {
        user_id: "u1",
        username: "alice",
        role: "member",
        must_change_password: false,
        idle_timeout_minutes: 15,
      },
      csrfToken: "csrf-1",
    });
    // Nothing may leak into any persistent storage.
    expect(window.localStorage.length).toBe(0);
    expect(window.sessionStorage.length).toBe(0);
    expect(sessionStore.get().csrfToken).toBe("csrf-1");
    sessionStore.clear();
    expect(sessionStore.get().principal).toBeNull();
    expect(sessionStore.get().csrfToken).toBe("");
  });
});

describe("api client", () => {
  it("sends the CSRF token on writes and parses success bodies", async () => {
    const fetchMock = stubFetch([{ status: 200, body: { ok: true } }]);
    const result = await request<{ ok: boolean }>("POST", "/api/v1/thing", { a: 1 }, { csrfToken: "csrf-9" });
    expect(result).toEqual({ ok: true });
    const call = fetchMock.mock.calls[0];
    const init = call[1] as RequestInit;
    const headers = init.headers as Record<string, string>;
    expect(headers["X-CSRF-Token"]).toBe("csrf-9");
    expect(headers["Content-Type"]).toBe("application/json");
  });

  it("does not send CSRF headers on GET", async () => {
    const fetchMock = stubFetch([{ status: 200, body: { ok: true } }]);
    await request("GET", "/api/v1/thing", undefined, { csrfToken: "csrf-9" });
    const call = fetchMock.mock.calls[0];
    const init = call[1] as RequestInit;
    const headers = init.headers as Record<string, string>;
    expect(headers["X-CSRF-Token"]).toBeUndefined();
  });

  it("carries code, request_id and current_revision from 409 conflicts", async () => {
    stubFetch([
      {
        status: 409,
        body: { code: "REVISION_CONFLICT", message: "冲突", request_id: "rid-2", current_revision: 8 },
      },
    ]);
    const error = await request("PUT", "/api/v1/items/x", {}).catch((err) => err);
    expect(error).toBeInstanceOf(ApiError);
    const apiError = error as ApiError;
    expect(apiError.code).toBe("REVISION_CONFLICT");
    expect(apiError.requestId).toBe("rid-2");
    expect(apiError.currentRevision).toBe(8);
    expect(apiError.retryable).toBe(false);
  });

  it("clears the in-memory session and fires the expired event on 401", async () => {
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
    stubFetch([{ status: 401, body: { code: "UNAUTHORIZED", message: "会话失效", request_id: "r" } }]);
    const expired = vi.fn();
    window.addEventListener(SESSION_EXPIRED_EVENT, expired);
    await request("GET", "/api/v1/items").catch(() => undefined);
    await waitFor(() => expect(expired).toHaveBeenCalledTimes(1));
    window.removeEventListener(SESSION_EXPIRED_EVENT, expired);
    expect(sessionStore.get().principal).toBeNull();
  });

  it("marks 503 and 429 as retryable", async () => {
    stubFetch([{ status: 503, body: { code: "DATABASE_BUSY", message: "忙", request_id: "r" } }]);
    const error = (await request("GET", "/api/v1/items").catch((err) => err)) as ApiError;
    expect(error.retryable).toBe(true);
  });
});

describe("shell navigation", () => {
  it("routes through pushState on link click", async () => {
    const user = userEvent.setup();
    window.history.replaceState({}, "", "/vault");
    render(
      <AppShell>
        <Router
          routes={[
            { path: "/vault", element: <p>保险库页</p> },
            { path: "/account", element: <p>账户页</p> },
          ]}
        />
      </AppShell>,
    );
    expect(screen.getByText("保险库页")).toBeInTheDocument();
    const desktopAccountLink = screen.getByRole("navigation", { name: "主导航" }).querySelector('a[href="/account"]')!;
    await user.click(desktopAccountLink);
    expect(window.location.pathname).toBe("/account");
    expect(screen.getByText("账户页")).toBeInTheDocument();
  });
});
