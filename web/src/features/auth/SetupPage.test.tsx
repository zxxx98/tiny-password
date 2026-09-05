import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { SetupPage } from "./SetupPage";

function mockFetch(handlers: {
  status: { initialized: boolean };
  csrf?: { csrf_token: string };
  init?: { status: number; body: unknown };
}) {
  return vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.endsWith("/api/v1/setup/status")) {
      return new Response(JSON.stringify(handlers.status), { status: 200 });
    }
    if (url.endsWith("/api/v1/csrf")) {
      return new Response(JSON.stringify(handlers.csrf ?? { csrf_token: "test-csrf" }), {
        status: 200,
      });
    }
    if (url.endsWith("/api/v1/setup/init")) {
      return new Response(JSON.stringify(handlers.init?.body ?? {}), {
        status: handlers.init?.status ?? 200,
      });
    }
    throw new Error(`unexpected fetch: ${url}`);
  });
}

beforeEach(() => {
  vi.stubGlobal("fetch", vi.fn());
});
afterEach(() => {
  vi.unstubAllGlobals();
});

describe("SetupPage", () => {
  it("renders the form with persistent labels on an uninitialized instance", async () => {
    vi.stubGlobal("fetch", mockFetch({ status: { initialized: false } }));

    render(<SetupPage />);

    expect(await screen.findByText("初始化令牌")).toBeInTheDocument();
    expect(screen.getByLabelText("初始化令牌")).toBeInTheDocument();
    expect(screen.getByLabelText("管理员用户名")).toBeInTheDocument();
    expect(screen.getByLabelText("管理员密码")).toBeInTheDocument();
    expect(screen.getByLabelText("确认密码")).toBeInTheDocument();
    // Secrets must not be persisted by the browser's form autofill.
    expect(screen.getByLabelText("初始化令牌")).toHaveAttribute("autocomplete", "off");
    expect(screen.getByLabelText("管理员密码")).toHaveAttribute("autocomplete", "new-password");
  });

  it("shows the initialized notice instead of the form", async () => {
    vi.stubGlobal("fetch", mockFetch({ status: { initialized: true } }));

    render(<SetupPage />);

    expect(await screen.findByText("本实例已完成初始化")).toBeInTheDocument();
    expect(screen.queryByLabelText("初始化令牌")).not.toBeInTheDocument();
  });

  it("submits the setup payload and shows the login guidance on success", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/api/v1/setup/status")) {
        return new Response(JSON.stringify({ initialized: false }), { status: 200 });
      }
      if (url.endsWith("/api/v1/csrf")) {
        return new Response(JSON.stringify({ csrf_token: "tok-123" }), { status: 200 });
      }
      if (url.endsWith("/api/v1/setup/init")) {
        const headers = new Headers(init?.headers);
        expect(headers.get("X-CSRF-Token")).toBe("tok-123");
        const payload = JSON.parse(String(init?.body)) as {
          token: string;
          username: string;
          password: string;
        };
        expect(payload).toEqual({
          token: "tok-abc",
          username: "admin",
          password: "correct horse battery 42",
        });
        return new Response(JSON.stringify({ initialized: true, username: "admin" }), {
          status: 200,
        });
      }
      throw new Error(`unexpected fetch: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const user = userEvent.setup();
    render(<SetupPage />);

    await user.type(await screen.findByLabelText("初始化令牌"), "tok-abc");
    await user.type(screen.getByLabelText("管理员用户名"), "admin");
    await user.type(screen.getByLabelText("管理员密码"), "correct horse battery 42");
    await user.type(screen.getByLabelText("确认密码"), "correct horse battery 42");
    await user.click(screen.getByRole("button", { name: "创建管理员" }));

    await waitFor(() => {
      expect(screen.getByText("管理员已创建")).toBeInTheDocument();
    });
    expect(screen.getByRole("link", { name: "前往登录" })).toHaveAttribute("href", "/login");
  });

  it("shows the token error state without succeeding", async () => {
    vi.stubGlobal(
      "fetch",
      mockFetch({
        status: { initialized: false },
        init: {
          status: 401,
          body: {
            code: "SETUP_TOKEN_INVALID",
            message: "the setup token is wrong",
            request_id: "req-1",
          },
        },
      }),
    );

    const user = userEvent.setup();
    render(<SetupPage />);

    await user.type(await screen.findByLabelText("初始化令牌"), "wrong-token");
    await user.type(screen.getByLabelText("管理员用户名"), "admin");
    await user.type(screen.getByLabelText("管理员密码"), "correct horse battery 42");
    await user.type(screen.getByLabelText("确认密码"), "correct horse battery 42");
    await user.click(screen.getByRole("button", { name: "创建管理员" }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("初始化令牌错误");
    expect(screen.queryByText("管理员已创建")).not.toBeInTheDocument();
  });

  it("blocks submission when the passwords do not match", async () => {
    vi.stubGlobal(
      "fetch",
      mockFetch({
        status: { initialized: false },
        init: { status: 200, body: { initialized: true } },
      }),
    );

    const user = userEvent.setup();
    render(<SetupPage />);

    await user.type(await screen.findByLabelText("初始化令牌"), "tok");
    await user.type(screen.getByLabelText("管理员用户名"), "admin");
    await user.type(screen.getByLabelText("管理员密码"), "correct horse battery 42");
    await user.type(screen.getByLabelText("确认密码"), "different password 42");
    await user.click(screen.getByRole("button", { name: "创建管理员" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("两次输入的密码不一致");
    expect(screen.queryByText("管理员已创建")).not.toBeInTheDocument();
  });
});
