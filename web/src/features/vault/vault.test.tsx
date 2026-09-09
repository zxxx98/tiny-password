import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { HistoryPage } from "./HistoryPage";
import { ItemDetail } from "./ItemDetail";
import { ItemEditor } from "./ItemEditor";
import { ItemDialog } from "./ItemDialog";
import { SensitiveField } from "./SensitiveField";
import { TrashPage } from "./TrashPage";
import { VaultPage } from "./VaultPage";
import { LoginFields } from "./forms/LoginFields";
import { sessionStore } from "../../app/session";
import type { ItemDetail as ItemDetailData } from "./types";

const csrf = "csrf-token";
const principal = {
  user_id: "user-alice",
  username: "alice",
  role: "member" as const,
  must_change_password: false,
  idle_timeout_minutes: 15,
};

type Stub = { status: number; body?: unknown; headers?: Record<string, string> };

function stubFetch(steps: Stub[]) {
  const queue = [...steps];
  const fetchMock = vi.fn(async (_url: string | URL, init?: RequestInit): Promise<Response> => {
    void init;
    const step = queue.shift() ?? { status: 200, body: { items: [], next_cursor: null } };
    if (step.status === 204) {
      return new Response(null, { status: 204 });
    }
    return new Response(JSON.stringify(step.body ?? {}), {
      status: step.status,
      headers: { "Content-Type": "application/json", ...step.headers },
    });
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

const meta = {
  id: "item-1",
  title: "Family bank",
  item_type: "login" as const,
  vault_scope: "personal" as const,
  owner_id: "user-alice",
  favorite: false,
  revision: 1,
  created_at: "2026-09-05T10:00:00.000000000Z",
  updated_at: "2026-09-05T10:00:00.000000000Z",
  deleted_at: null,
};

const loginDetail: ItemDetailData = {
  ...meta,
  tags: ["bank"],
  payload: { name: "Family bank", username: "alice", password: "SYNSECRET-pw", notes: "n" },
};

function stubClipboard(impl?: Partial<Clipboard>) {
  const clipboard: Clipboard = {
    writeText: vi.fn(async () => undefined),
    readText: vi.fn(async () => "SYNSECRET-pw"),
    ...impl,
  } as unknown as Clipboard;
  Object.defineProperty(window.navigator, "clipboard", { value: clipboard, configurable: true });
  return clipboard;
}

beforeEach(() => {
  sessionStore.set({ principal, csrfToken: csrf });
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
  window.history.replaceState({}, "", "/");
  sessionStore.clear();
});

describe("SensitiveField", () => {
  it("masks by default, audits the reveal, and re-masks after 30 seconds", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const fetchMock = stubFetch([{ status: 204 }, { status: 204 }]);
    render(<SensitiveField label="密码" field="password" value="SYNSECRET-pw" itemId="item-1" csrfToken={csrf} />);

    // Masked: the plaintext is not rendered anywhere, and no audit fired yet.
    expect(screen.getByTestId("secret-masked-password")).toHaveTextContent("••••••••");
    expect(screen.queryByTestId("secret-value-password")).not.toBeInTheDocument();
    expect(document.body.textContent).not.toContain("SYNSECRET-pw");

    await user.click(screen.getByRole("button", { name: "显示" }));
    expect(screen.getByTestId("secret-value-password")).toHaveTextContent("SYNSECRET-pw");
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    const [revealUrl, revealInit] = fetchMock.mock.calls[0];
    expect(revealUrl).toBe("/api/v1/items/item-1/reveal");
    expect(JSON.parse((revealInit as RequestInit).body as string)).toEqual({ field: "password" });

    // 30 seconds later the field re-masks itself.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(30_000);
    });
    expect(screen.queryByTestId("secret-value-password")).not.toBeInTheDocument();
  });

  it("re-masks immediately on blur", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    vi.useFakeTimers({ shouldAdvanceTime: true });
    stubFetch([{ status: 204 }]);
    render(<SensitiveField label="密码" field="password" value="SYNSECRET-pw" itemId="item-1" csrfToken={csrf} />);
    await user.click(screen.getByRole("button", { name: "显示" }));
    const output = screen.getByTestId("secret-value-password");
    output.focus();
    await user.keyboard("{Escape}");
    await act(async () => {
      output.blur();
    });
    expect(screen.queryByTestId("secret-value-password")).not.toBeInTheDocument();
  });

  it("audits the copy with the field category only and clears the clipboard after 30s", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const clipboard = stubClipboard();
    const fetchMock = stubFetch([{ status: 204 }, { status: 204 }]);
    render(<SensitiveField label="密码" field="password" value="SYNSECRET-pw" itemId="item-1" csrfToken={csrf} />);

    await user.click(screen.getByRole("button", { name: "复制" }));
    expect(clipboard.writeText).toHaveBeenCalledWith("SYNSECRET-pw");
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    expect(JSON.parse((fetchMock.mock.calls[0][1] as RequestInit).body as string)).toEqual({ field: "password" });
    expect(screen.getByRole("status")).toHaveTextContent("已复制");

    await act(async () => {
      await vi.advanceTimersByTimeAsync(30_000);
    });
    await waitFor(() => expect(clipboard.writeText).toHaveBeenLastCalledWith(""));
    expect(await screen.findByText("剪贴板已清除。")).toBeInTheDocument();
  });

  it("is honest when the browser refuses clipboard access", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    vi.useFakeTimers({ shouldAdvanceTime: true });
    stubClipboard({ writeText: vi.fn(async () => { throw new Error("denied"); }) });
    stubFetch([{ status: 204 }]);
    render(<SensitiveField label="密码" field="password" value="SYNSECRET-pw" itemId="item-1" csrfToken={csrf} />);
    await user.click(screen.getByRole("button", { name: "复制" }));
    expect(await screen.findByText(/浏览器不允许写入剪贴板/)).toBeInTheDocument();
  });

  it("keeps clipboard cleanup scheduled after the field unmounts", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const clipboard = stubClipboard();
    stubFetch([{ status: 204 }]);
    const { unmount } = render(
      <SensitiveField label="密码" field="password" value="SYNSECRET-pw" itemId="item-1" csrfToken={csrf} />,
    );
    await user.click(screen.getByRole("button", { name: "复制" }));
    unmount();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(30_000);
    });
    await waitFor(() => expect(clipboard.writeText).toHaveBeenLastCalledWith(""));
  });
});

describe("ItemEditor", () => {
  it("prefills a personal login editor from an in-memory generated password draft", () => {
    render(
      <ItemEditor
        csrfToken={csrf}
        initialLoginDraft={{ password: "HANDOFF-PASSWORD" }}
        onSaved={() => {}}
        onCancel={() => {}}
      />,
    );
    expect(screen.getByLabelText("密码")).toHaveValue("HANDOFF-PASSWORD");
    expect(screen.getByLabelText("密码")).toHaveAttribute("type", "password");
  });

  it("blocks submission client-side when required fields are missing", async () => {
    const user = userEvent.setup();
    const fetchMock = stubFetch([]);
    render(<ItemEditor csrfToken={csrf} onSaved={() => {}} onCancel={() => {}} />);
    await user.click(screen.getByRole("button", { name: "创建条目" }));
    expect(await screen.findByText("名称必填。")).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("creates a login with an idempotency key and the typed payload", async () => {
    const user = userEvent.setup();
    const fetchMock = stubFetch([
      { status: 201, body: { ...loginDetail, revision: 1 } },
    ]);
    const onSaved = vi.fn();
    render(<ItemEditor csrfToken={csrf} onSaved={onSaved} onCancel={() => {}} />);
    await user.type(screen.getByLabelText("名称"), "Family bank");
    await user.type(screen.getByLabelText("用户名"), "alice");
    await user.type(screen.getByLabelText("密码"), "SYNSECRET-pw");
    await user.click(screen.getByRole("button", { name: "创建条目" }));

    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1));
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/api/v1/items");
    expect((init as RequestInit).headers).toMatchObject({ "Idempotency-Key": expect.any(String) });
    const body = JSON.parse((init as RequestInit).body as string);
    expect(body).toMatchObject({
      item_type: "login",
      vault_scope: "personal",
      payload: { name: "Family bank", username: "alice", password: "SYNSECRET-pw" },
    });
  });

  it("keeps the edits on screen when the server answers 409 REVISION_CONFLICT", async () => {
    const user = userEvent.setup();
    const fetchMock = stubFetch([
      {
        status: 409,
        body: { code: "REVISION_CONFLICT", message: "conflict", request_id: "r-1", current_revision: 7 },
      },
    ]);
    render(<ItemEditor csrfToken={csrf} initial={loginDetail} onSaved={() => {}} onCancel={() => {}} />);
    const nameInput = screen.getByLabelText("名称");
    await user.clear(nameInput);
    await user.type(nameInput, "Renamed by me");
    await user.click(screen.getByRole("button", { name: "保存修改" }));

    // The conflict panel appears with the current revision; the edit stays.
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("当前版本 7");
    expect((screen.getByLabelText("名称") as HTMLInputElement).value).toBe("Renamed by me");
    // Reloading the authoritative content pulls the stored payload.
    await user.click(screen.getByRole("button", { name: "重新加载服务端内容" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(fetchMock.mock.calls[1][0]).toBe("/api/v1/items/item-1");
  });

  it("uses the refreshed revision after reloading a conflict", async () => {
    const user = userEvent.setup();
    const fresh = { ...loginDetail, revision: 7, payload: { ...loginDetail.payload, name: "Server copy" } };
    const fetchMock = stubFetch([
      { status: 409, body: { code: "REVISION_CONFLICT", message: "conflict", request_id: "r-1", current_revision: 7 } },
      { status: 200, body: fresh },
      { status: 200, body: fresh },
    ]);
    render(<ItemEditor csrfToken={csrf} initial={loginDetail} onSaved={() => {}} onCancel={() => {}} />);
    await user.click(screen.getByRole("button", { name: "保存修改" }));
    await screen.findByRole("alert");
    await user.click(screen.getByRole("button", { name: "重新加载服务端内容" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    await user.click(screen.getByRole("button", { name: "保存修改" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));
    const body = JSON.parse((fetchMock.mock.calls[2][1] as RequestInit).body as string);
    expect(body.revision).toBe(7);
  });

  it("reports editor dirty state after a draft field changes", async () => {
    const user = userEvent.setup();
    const onDirtyChange = vi.fn();
    render(
      <ItemEditor
        csrfToken={csrf}
        initial={loginDetail}
        onSaved={() => {}}
        onCancel={() => {}}
        onDirtyChange={onDirtyChange}
      />,
    );
    expect(onDirtyChange).toHaveBeenLastCalledWith(false);
    await user.clear(screen.getByLabelText("名称"));
    await user.type(screen.getByLabelText("名称"), "Renamed");
    expect(onDirtyChange).toHaveBeenLastCalledWith(true);
  });

  it("re-masks after reveal even when copy is used", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    vi.useFakeTimers({ shouldAdvanceTime: true });
    stubClipboard();
    stubFetch([{ status: 204 }, { status: 204 }]);
    render(<SensitiveField label="密码" field="password" value="SYNSECRET-pw" itemId="item-1" csrfToken={csrf} />);
    await user.click(screen.getByRole("button", { name: "显示" }));
    await user.click(screen.getByRole("button", { name: "复制" }));
    await act(async () => { await vi.advanceTimersByTimeAsync(30_000); });
    expect(screen.queryByTestId("secret-value-password")).not.toBeInTheDocument();
  });
});

describe("ItemDetail", () => {
  it.each([
    ["example.com", "https://example.com/"],
    [" example.com/login?q=1#form ", "https://example.com/login?q=1#form"],
    ["https://example.com/login", "https://example.com/login"],
    ["http://192.168.1.2:8080/login", "http://192.168.1.2:8080/login"],
    ["localhost:8080/login", "https://localhost:8080/login"],
    ["//example.com/login", "https://example.com/login"],
    ["javascript:alert(1)", null],
    ["data:text/html,test", null],
    ["ftp://example.com", null],
    ["/login", null],
    ["https://", null],
    ["not a domain", null],
  ])("renders saved URL %s with a safe absolute destination", (url, expected) => {
    render(<ItemDetail
      detail={{ ...loginDetail, payload: { ...loginDetail.payload, urls: [url] } }}
      csrfToken={csrf} canManage={true} onEdit={() => {}}
      onShowHistory={() => {}} onToggleFavorite={() => {}} onTrash={() => {}}
    />);
    if (expected) {
      expect(screen.getByRole("link", { name: url.trim() })).toHaveAttribute("href", expected);
    } else {
      expect(screen.queryByRole("link", { name: url.trim() })).not.toBeInTheDocument();
      expect(screen.getByText(url)).toBeInTheDocument();
    }
  });

  it("shows the scope text and the creator signature together", () => {
    const shared: ItemDetailData = {
      ...loginDetail,
      vault_scope: "shared",
      owner_id: undefined,
      creator_id: "user-bob",
      creator_name: "bob",
      payload: { name: "Shared wifi", body: "" },
      item_type: "secure_note",
    };
    render(
      <ItemDetail
        detail={shared}
        csrfToken={csrf}
        canManage={false}
        onEdit={() => {}}
        onShowHistory={() => {}}
        onToggleFavorite={() => {}}
        onTrash={() => {}}
      />,
    );
    expect(screen.getByText(/共享 · 创建者 bob/)).toBeInTheDocument();
    expect(screen.getByText(/只有创建者能修改/)).toBeInTheDocument();
    // Non-managers get no edit or trash controls.
    expect(screen.queryByRole("button", { name: "编辑" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "移入回收站" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "历史" })).toBeInTheDocument();
  });

  it("renders sensitive login fields masked and audited", async () => {
    const user = userEvent.setup();
    const fetchMock = stubFetch([{ status: 204 }]);
    render(
      <ItemDetail
        detail={loginDetail}
        csrfToken={csrf}
        canManage={true}
        onEdit={() => {}}
        onShowHistory={() => {}}
        onToggleFavorite={() => {}}
        onTrash={() => {}}
      />,
    );
    expect(screen.getByTestId("secret-masked-password")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "显示" }));
    expect(screen.getByTestId("secret-value-password")).toBeInTheDocument();
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
  });
});

describe("ItemDialog", () => {
  it("renders one accessible detail dialog with a scrollable body and action footer", () => {
    render(
      <ItemDialog
        mode={{ kind: "detail", detail: loginDetail }}
        csrfToken={csrf}
        canManage={true}
        onClose={() => {}}
        onEdit={() => {}}
        onShowHistory={() => {}}
        onToggleFavorite={() => {}}
        onTrash={() => {}}
      />,
    );

    expect(screen.getAllByRole("dialog")).toHaveLength(1);
    expect(screen.getByRole("dialog", { name: "Family bank" })).toBeInTheDocument();
    expect(screen.getByText(/登录凭据/)).toBeInTheDocument();
    expect(screen.getByText(/版本 1/)).toBeInTheDocument();
    expect(screen.getByTestId("dialog-body")).toHaveClass("overflow-y-auto");
    expect(screen.getByRole("button", { name: "编辑条目" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "历史记录" })).toBeInTheDocument();
  });

  it("renders loading and retryable error modes without stale item content", () => {
    const onRetry = vi.fn();
    const { rerender } = render(
      <ItemDialog mode={{ kind: "loading", itemId: "item-1" }} csrfToken={csrf} onClose={() => {}} />,
    );
    expect(screen.getByRole("status")).toHaveTextContent("正在解密条目");

    rerender(
      <ItemDialog
        mode={{ kind: "error", itemId: "item-1", message: "条目不存在。", requestId: "req-1" }}
        csrfToken={csrf}
        onClose={() => {}}
        onRetry={onRetry}
      />,
    );
    expect(screen.queryByText("Family bank")).not.toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent("条目不存在");
    expect(screen.getByRole("alert")).toHaveTextContent("request_id: req-1");
    expect(screen.getByRole("button", { name: "重试" })).toBeInTheDocument();
  });
});

describe("LoginFields URL probe", () => {
  it("probes on demand and lets the user apply a detected login URL", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    const fetchMock = stubFetch([
      {
        status: 200,
        body: {
          input_url: "https://example.com/",
          candidates: [{ url: "https://example.com/login", score: 10, password_field: true, reasons: ["检测到密码输入框"] }],
        },
      },
    ]);
    render(
      <LoginFields
        payload={{ name: "Example", username: "alice", password: "pw", urls: ["example.com"] }}
        errors={{}}
        onChange={onChange}
      />,
    );

    await user.click(screen.getByRole("button", { name: "查找登录页" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/items/login-page-probe");
    expect(JSON.parse((fetchMock.mock.calls[0][1] as RequestInit).body as string)).toEqual({ url: "example.com" });
    expect(await screen.findByText("https://example.com/login")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "使用此网址" }));
    expect(onChange).toHaveBeenLastCalledWith({ urls: ["https://example.com/login"] });
  });

  it("shows a no-match message when the server returns a null candidate list", async () => {
    const user = userEvent.setup();
    stubFetch([{ status: 200, body: { input_url: "https://baidu.com/", candidates: null } }]);
    render(
      <LoginFields
        payload={{ name: "Baidu", username: "alice", password: "pw", urls: ["baidu.com"] }}
        errors={{}}
        onChange={() => {}}
      />,
    );

    await user.click(screen.getByRole("button", { name: "查找登录页" }));
    expect(await screen.findByText("未找到明显的登录页，你仍可以保存当前网址。")).toBeInTheDocument();
  });
});

describe("VaultPage", () => {
  it("opens one responsive item dialog and restores the row focus when closed", async () => {
    const user = userEvent.setup();
    window.history.replaceState({}, "", "/vault");
    const fetchMock = stubFetch([
      { status: 200, body: { items: [{ ...meta }], next_cursor: null } },
      { status: 200, body: { weak: 0, reused: 0, expired: 0, items: [] } },
      { status: 200, body: loginDetail },
    ]);
    render(<VaultPage />);
    const row = await screen.findByRole("button", { name: /Family bank/ });
    await user.click(row);

    const dialog = await screen.findByRole("dialog", { name: "Family bank" });
    expect(screen.getAllByRole("dialog")).toHaveLength(1);
    expect(dialog).toContainElement(screen.getByTestId("secret-masked-password"));
    expect(screen.queryByTestId("mobile-detail")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "关闭弹窗" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(window.location.pathname).toBe("/vault");
    expect(row).toHaveFocus();
    expect(fetchMock.mock.calls[2][0]).toBe("/api/v1/items/item-1");
  });

  it("protects dirty edits from close and keeps the draft when discard is canceled", async () => {
    const user = userEvent.setup();
    window.history.replaceState({}, "", "/vault");
    stubFetch([
      { status: 200, body: { items: [{ ...meta }], next_cursor: null } },
      { status: 200, body: { weak: 0, reused: 0, expired: 0, items: [] } },
      { status: 200, body: loginDetail },
    ]);
    render(<VaultPage />);
    await user.click(await screen.findByRole("button", { name: /Family bank/ }));
    await user.click(await screen.findByRole("button", { name: "编辑条目" }));
    const title = screen.getByLabelText("名称");
    await user.clear(title);
    await user.type(title, "Draft title");
    await user.click(screen.getByRole("button", { name: "关闭弹窗" }));

    const discardDialog = await screen.findByRole("dialog", { name: "放弃未保存修改？" });
    await user.click(within(discardDialog).getByRole("button", { name: "取消" }));
    expect(screen.getByLabelText("名称")).toHaveValue("Draft title");
    expect(window.location.pathname).toBe("/vault/item-1");
  });

  it("returns to detail when the editor cancel discards a dirty draft", async () => {
    const user = userEvent.setup();
    window.history.replaceState({}, "", "/vault");
    stubFetch([
      { status: 200, body: { items: [{ ...meta }], next_cursor: null } },
      { status: 200, body: { weak: 0, reused: 0, expired: 0, items: [] } },
      { status: 200, body: loginDetail },
    ]);
    render(<VaultPage />);
    await user.click(await screen.findByRole("button", { name: /Family bank/ }));
    await user.click(await screen.findByRole("button", { name: "编辑条目" }));
    await user.type(screen.getByLabelText("名称"), " draft");
    await user.click(within(screen.getByRole("form", { name: "编辑条目" })).getByRole("button", { name: "取消" }));
    await user.click(within(screen.getByRole("dialog", { name: "放弃未保存修改？" })).getByRole("button", { name: "放弃修改" }));

    expect(await screen.findByRole("dialog", { name: "Family bank" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "编辑条目" })).toBeInTheDocument();
    expect(window.location.pathname).toBe("/vault/item-1");
  });

  it("treats an initially loaded detail URL as direct navigation when closing", async () => {
    const user = userEvent.setup();
    window.history.replaceState({ source: "list", modal: "item" }, "", "/vault/item-1");
    stubFetch([
      { status: 200, body: { items: [{ ...meta }], next_cursor: null } },
      { status: 200, body: { weak: 0, reused: 0, expired: 0, items: [] } },
      { status: 200, body: loginDetail },
    ]);
    render(<VaultPage />);
    await screen.findByRole("dialog", { name: "Family bank" });
    const historyBack = vi.spyOn(window.history, "back");
    await user.click(screen.getByRole("button", { name: "关闭弹窗" }));

    await waitFor(() => expect(window.location.pathname).toBe("/vault"));
    expect(historyBack).not.toHaveBeenCalled();
    historyBack.mockRestore();
  });

  it("restores a dirty detail route before asking about browser back", async () => {
    const user = userEvent.setup();
    window.history.replaceState({}, "", "/vault");
    window.history.pushState({ source: "list", modal: "item" }, "", "/vault/item-1");
    stubFetch([
      { status: 200, body: { items: [{ ...meta }], next_cursor: null } },
      { status: 200, body: { weak: 0, reused: 0, expired: 0, items: [] } },
      { status: 200, body: loginDetail },
    ]);
    render(<VaultPage />);
    await user.click(await screen.findByRole("button", { name: "编辑条目" }));
    await user.type(screen.getByLabelText("名称"), " draft");

    const historyGo = vi.spyOn(window.history, "go").mockImplementation(() => {});
    window.history.replaceState({}, "", "/vault");
    window.dispatchEvent(new PopStateEvent("popstate"));

    await screen.findByRole("dialog", { name: "放弃未保存修改？" });
    expect(historyGo).toHaveBeenCalledWith(1);

    window.history.replaceState({}, "", "/vault/item-1");
    window.dispatchEvent(new PopStateEvent("popstate"));
    await user.click(within(screen.getByRole("dialog", { name: "放弃未保存修改？" })).getByRole("button", { name: "取消" }));
    historyGo.mockRestore();
  });

  it("renders the workspace grid with list titles, types and owner signature", async () => {
    stubFetch([
      {
        status: 200,
        body: {
          items: [
            { ...meta, title: "Family bank" },
            {
              ...meta,
              id: "item-2",
              title: "Shared wifi",
              vault_scope: "shared",
              owner_id: undefined,
              creator_id: "user-bob",
              creator_name: "bob",
            },
          ],
          next_cursor: null,
        },
      },
      { status: 200, body: { weak: 0, reused: 0, expired: 0, items: [] } },
    ]);
    render(<VaultPage />);
    await waitFor(() => expect(screen.getByText("Family bank")).toBeInTheDocument());
    expect(screen.getByText("Shared wifi")).toBeInTheDocument();
    expect(screen.getByText(/共享 · 创建者 bob/)).toBeInTheDocument();
    expect(screen.getByText(/个人/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "新建条目" })).toBeInTheDocument();
  });

  it("keeps one new-item editor reachable on narrow screens", async () => {
    const user = userEvent.setup();
    vi.stubGlobal("matchMedia", () => ({ matches: false, addListener: () => {}, removeListener: () => {} }));
    stubFetch([
      { status: 200, body: { items: [], next_cursor: null } },
      { status: 200, body: { weak: 0, reused: 0, expired: 0, items: [] } },
    ]);
    render(<VaultPage />);
    await user.click(await screen.findByRole("button", { name: "新建条目" }));
    expect(screen.getAllByRole("form", { name: "新建条目" })).toHaveLength(1);
  });

  it("opens the new-item editor when crypto.randomUUID is unavailable", async () => {
    const user = userEvent.setup();
    vi.stubGlobal("crypto", {
      getRandomValues: (values: Uint8Array) => {
        values.fill(1);
        return values;
      },
    });
    stubFetch([
      { status: 200, body: { items: [], next_cursor: null } },
      { status: 200, body: { weak: 0, reused: 0, expired: 0, items: [] } },
    ]);
    render(<VaultPage />);
    await user.click(await screen.findByRole("button", { name: "新建条目" }));
    expect(screen.getAllByRole("form", { name: "新建条目" })).toHaveLength(1);
  });

  it("shows the empty-vault and search-no-hit states", async () => {
    const user = userEvent.setup();
    stubFetch([
      { status: 200, body: { items: [], next_cursor: null } },
      { status: 200, body: { items: [], next_cursor: null } },
    ]);
    render(<VaultPage />);
    expect(await screen.findByText(/保险库还是空的/)).toBeInTheDocument();
    await user.type(screen.getByLabelText("搜索条目"), "nothing-matches");
    await user.click(screen.getByRole("button", { name: "搜索" }));
    expect(await screen.findByText(/没有匹配的条目/)).toBeInTheDocument();
  });

  it("surfaces network failures with the error summary", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new TypeError("network down");
      }),
    );
    render(<VaultPage />);
    expect(await screen.findByText(/网络错误，请重试/)).toBeInTheDocument();
  });

  it("shows the health banner when findings exist", async () => {
    stubFetch([
      { status: 200, body: { items: [], next_cursor: null } },
      { status: 200, body: { weak: 2, reused: 1, expired: 0, items: [] } },
    ]);
    render(<VaultPage />);
    expect(await screen.findByText(/弱密码 2 · 重复 1 · 已过期 0/)).toBeInTheDocument();
  });
});

describe("HistoryPage", () => {
  it("lists revisions and restores one into a new current revision", async () => {
    const user = userEvent.setup();
    const fetchMock = stubFetch([
      {
        status: 200,
        body: {
          items: [
            { revision: 2, updated_at: "2026-09-05T11:00:00.000000000Z" },
            { revision: 1, updated_at: "2026-09-05T10:00:00.000000000Z" },
          ],
          next_cursor: null,
        },
      },
      { status: 200, body: { ...loginDetail, revision: 3 } },
    ]);
    const onRestored = vi.fn();
    render(<HistoryPage itemId="item-1" csrfToken={csrf} canManage={true} onRestored={onRestored} onClose={() => {}} />);
    expect(await screen.findByText(/版本 2/)).toBeInTheDocument();
    expect(screen.getByText(/版本 1/)).toBeInTheDocument();

    const row = screen.getByText(/版本 1/).closest("li")!;
    await user.click(within(row).getByRole("button", { name: "恢复此版本" }));
    await user.click(await screen.findByRole("button", { name: "恢复" }));
    await waitFor(() => expect(onRestored).toHaveBeenCalledTimes(1));
    expect(fetchMock.mock.calls[1][0]).toBe("/api/v1/items/item-1/history/1/restore");
  });

  it("hides restore controls for a read-only shared item", async () => {
    stubFetch([{
      status: 200,
      body: { items: [{ revision: 1, updated_at: "2026-09-05T10:00:00.000000000Z" }], next_cursor: null },
    }]);
    render(<HistoryPage itemId="item-1" csrfToken={csrf} canManage={false} onRestored={() => {}} onClose={() => {}} />);
    await screen.findByText(/版本 1/);
    expect(screen.queryByRole("button", { name: "恢复此版本" })).not.toBeInTheDocument();
    expect(screen.getByText("仅可查看")).toBeInTheDocument();
  });
});

describe("TrashPage", () => {
  it("lists trashed items and restores after confirmation", async () => {
    const user = userEvent.setup();
    const fetchMock = stubFetch([
      { status: 200, body: { items: [{ ...meta, deleted_at: "2026-09-05T12:00:00.000000000Z" }], next_cursor: null } },
      { status: 200, body: { ...loginDetail } },
      { status: 200, body: { items: [], next_cursor: null } },
    ]);
    const onChanged = vi.fn();
    render(<TrashPage csrfToken={csrf} onChanged={onChanged} />);
    expect(await screen.findByText("Family bank")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "恢复" }));
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent("内容与版本号不变");
    await user.click(within(dialog).getByRole("button", { name: "恢复" }));
    await waitFor(() => expect(onChanged).toHaveBeenCalledTimes(1));
    expect(fetchMock.mock.calls[1][0]).toBe("/api/v1/items/item-1/restore");
  });

  it("warns about irreversibility before an early purge", async () => {
    const user = userEvent.setup();
    const fetchMock = stubFetch([
      { status: 200, body: { items: [{ ...meta, deleted_at: "2026-09-05T12:00:00.000000000Z" }], next_cursor: null } },
      { status: 204 },
      { status: 200, body: { items: [], next_cursor: null } },
    ]);
    render(<TrashPage csrfToken={csrf} onChanged={() => {}} />);
    expect(await screen.findByText("Family bank")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "彻底删除" }));
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent("无法通过产品恢复");
    await user.click(within(dialog).getByRole("button", { name: "彻底删除" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));
    expect(fetchMock.mock.calls[1][0]).toBe("/api/v1/items/item-1/purge");
  });
});
