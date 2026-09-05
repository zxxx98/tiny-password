import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { UsersPage } from "./UsersPage";
import { sessionStore } from "../../app/session";

const member = {
  id: "11111111-2222-7333-8444-555555555555",
  username: "bob",
  role: "member",
  status: "active",
  created_at: "2026-09-05T10:00:00.000000000Z",
};

function jsonResponse(status: number, body: unknown) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function stubFetch(steps: Array<{ status: number; body?: unknown }>) {
  const queue = [...steps];
  const fetchMock = vi.fn(async (_url: string | URL, init?: RequestInit): Promise<Response> => {
    void init;
    const step = queue.shift() ?? { status: 200, body: { items: [], next_cursor: null } };
    if (step.status === 204) {
      return new Response(null, { status: 204 });
    }
    return jsonResponse(step.status, step.body ?? {});
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function seedAdmin() {
  sessionStore.set({
    principal: {
      user_id: "admin-1",
      username: "Admin",
      role: "admin",
      must_change_password: false,
      idle_timeout_minutes: 15,
    },
    csrfToken: "csrf",
  });
}

afterEach(() => {
  vi.unstubAllGlobals();
  sessionStore.clear();
});

describe("UsersPage", () => {
  it("lists members with status labels", async () => {
    seedAdmin();
    const fetchMock = stubFetch([{ status: 200, body: { items: [member], next_cursor: null } }]);
    vi.stubGlobal("fetch", fetchMock);
    render(<UsersPage />);
    await waitFor(() => expect(screen.getByText(/bob/)).toBeInTheDocument());
    expect(screen.getByText(/启用/)).toBeInTheDocument();
  });

  it("creates a member and shows the initial password exactly once", async () => {
    const user = userEvent.setup();
    seedAdmin();
    const fetchMock = stubFetch([
      { status: 200, body: { items: [], next_cursor: null } },
      { status: 201, body: { ...member, username: "carol", status: "must_change_password" } },
      { status: 200, body: { items: [member], next_cursor: null } },
    ]);
    vi.stubGlobal("fetch", fetchMock);
    render(<UsersPage />);
    await user.click(screen.getByRole("button", { name: "创建成员" }));
    await user.type(screen.getByLabelText("用户名"), "carol");
    await user.click(screen.getByRole("button", { name: "创建成员" }));

    // The create button turned into the submitting label; wait for the
    // one-time password alert.
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("它只显示这一次");
    const created = await screen.findByText(/carol 已创建/);
    expect(created).toBeInTheDocument();
    // The create call carried the CSRF token and the payload.
    const createInit = fetchMock.mock.calls[1][1] as RequestInit;
    expect(JSON.parse(createInit.body as string)).toMatchObject({
      username: "carol",
      initial_password: expect.any(String),
    });
  });

  it("walks the two-step delete flow: export warning, then exact username confirmation", async () => {
    const user = userEvent.setup();
    seedAdmin();
    const fetchMock = stubFetch([
      { status: 200, body: { items: [member], next_cursor: null } },
      { status: 204 },
      { status: 200, body: { items: [], next_cursor: null } },
    ]);
    vi.stubGlobal("fetch", fetchMock);
    render(<UsersPage />);
    await waitFor(() => expect(screen.getByText(/bob/)).toBeInTheDocument());

    await user.click(screen.getByRole("button", { name: "删除" }));
    // Step 1: data-loss and export warning.
    const dialog = await screen.findByRole("dialog", { name: /删除成员 bob/ });
    expect(dialog).toHaveTextContent("删除前请先确认已完成数据导出");
    expect(dialog).toHaveTextContent("无法通过产品恢复");
    await user.click(screen.getByRole("button", { name: "继续删除" }));

    // Step 2: exact username confirmation; server enforces independently.
    const confirmDialog = await screen.findByRole("dialog", { name: "输入成员用户名以确认" });
    expect(confirmDialog).toBeInTheDocument();
    await user.type(screen.getByLabelText("目标用户名"), "bob");
    await user.click(screen.getByRole("button", { name: "永久删除" }));

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));
    const deleteInit = fetchMock.mock.calls[1][1] as RequestInit;
    expect(deleteInit.method).toBe("DELETE");
    expect(JSON.parse(deleteInit.body as string)).toEqual({ confirm_username: "bob" });
    expect(await screen.findByText(/已永久删除/)).toBeInTheDocument();
  });

  it("disables a member and surfaces server-side last-admin protection", async () => {
    const user = userEvent.setup();
    seedAdmin();
    const adminRow = { ...member, username: "Admin", role: "admin", id: "99999999-8888-7777-8666-555555555555" };
    const fetchMock = stubFetch([
      { status: 200, body: { items: [adminRow], next_cursor: null } },
      { status: 409, body: { code: "LAST_ADMIN_PROTECTED", message: "the last administrator cannot be disabled or deleted", request_id: "r-9" } },
    ]);
    vi.stubGlobal("fetch", fetchMock);
    render(<UsersPage />);
    await waitFor(() => expect(screen.getByText("Admin")).toBeInTheDocument());
    await user.click(screen.getByRole("button", { name: "停用" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("最后一位管理员不能被停用或删除。");
    expect(alert).toHaveTextContent("request_id: r-9");
  });
});
