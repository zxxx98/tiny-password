import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { sessionStore } from "../../app/session";
import { ItemEditor } from "./ItemEditor";
import { VaultPage } from "./VaultPage";
import { BulkActions } from "./BulkActions";
import { QuickCopy } from "./QuickCopy";
import { clearClipboardCleanup } from "./clipboardCleanup";
import { abortInFlightRequests } from "../../app/api";
import type { ItemDetail, ItemMeta } from "./types";

const csrf = "test-csrf";
const meta: ItemMeta = { id: "one", title: "Work account", item_type: "login", vault_scope: "personal", owner_id: "alice", revision: 1, favorite: false, created_at: "2026-09-01", updated_at: "2026-09-02", deleted_at: null, tags: ["work", "bank"] };
const second: ItemMeta = { ...meta, id: "two", title: "Home account", tags: ["home"] };
const detail: ItemDetail = { ...meta, tags: ["work", "bank"], payload: { name: meta.title, username: "alice", password: "SYNSECRET-password", notes: "Keep me" } };
const healthy = { weak: 0, reused: 0, expired: 0, items: [] };
function json(body: unknown, status = 200) { return new Response(status === 204 ? null : JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }); }
function api(handler?: (url: string, init: RequestInit) => Response | Promise<Response> | undefined) {
  const fetchMock = vi.fn(async (url: string, init: RequestInit) => {
    const custom = await handler?.(url, init);
    if (custom) return custom;
    if (url === "/api/v1/items/browse") return json({ items: [meta, second], next_cursor: null });
    if (url === "/api/v1/items/browse/tags") return json({ tags: ["work", "bank", "home"] });
    if (url === "/api/v1/items/health") return json(healthy);
    if (url === "/api/v1/items/one") return json(detail);
    if (url === "/api/v1/items/two") return json({ ...detail, ...second });
    return json({}, 204);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}
beforeEach(() => {
  window.history.replaceState({}, "", "/vault");
  sessionStore.set({ principal: { user_id: "alice", username: "alice", role: "member", must_change_password: false, idle_timeout_minutes: 15 }, csrfToken: csrf });
});
afterEach(() => { clearClipboardCleanup(); vi.unstubAllGlobals(); vi.useRealTimers(); sessionStore.clear(); window.history.replaceState({}, "", "/"); });

describe("Vault management", () => {
  it("combines sort, favorite, query and tag filters in body-carried requests and groups tags", async () => {
    const user = userEvent.setup(); const fetchMock = api(); render(<VaultPage />);
    await screen.findByText(meta.title);
    await user.selectOptions(screen.getByLabelText("排序"), "title_asc");
    await user.selectOptions(screen.getByLabelText("按标签筛选"), "work");
    await user.click(screen.getByRole("button", { name: "收藏" }));
    await user.type(screen.getByLabelText("搜索条目"), "account");
    await waitFor(() => {
      const calls = fetchMock.mock.calls.filter(([url]) => url === "/api/v1/items/browse");
      expect(JSON.parse(calls.at(-1)![1].body as string)).toMatchObject({ query: "account", tag: "work", favorite: true, sort: "title_asc" });
    });
    await user.selectOptions(screen.getByLabelText("分组"), "tag");
    expect(screen.getByRole("heading", { name: "work · 1" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "bank · 1" })).toBeInTheDocument();
    expect(screen.queryByText("SYNSECRET-password")).not.toBeInTheDocument();
  });

  it("keeps only failed items selected after a partially successful batch", async () => {
    const user = userEvent.setup();
    const fetchMock = api((url, init) => {
      if (init.method === "DELETE" && url.endsWith("/one")) return json({}, 204);
      if (init.method === "DELETE" && url.endsWith("/two")) return json({ code: "DATABASE_BUSY", message: "请稍后重试" }, 503);
    });
    render(<VaultPage />); await screen.findByText(meta.title);
    await user.click(screen.getByLabelText("选择已加载的全部条目"));
    await user.click(screen.getByRole("button", { name: "批量移入回收站" }));
    expect(fetchMock.mock.calls.filter(([, init]) => init.method === "DELETE")).toHaveLength(0);
    await user.click(screen.getByRole("button", { name: "确认执行" }));
    await screen.findByText(/完成 1 项，失败 1 项/);
    expect(screen.getByLabelText(`选择 ${meta.title}`)).not.toBeChecked();
    expect(screen.getByLabelText(`选择 ${second.title}`)).toBeChecked();
    expect(screen.getByText(/Home account：请稍后重试/)).toBeInTheDocument();
  });

  it.each(["filter", "search"])("blocks old selections during a delayed %s response and batches only current results", async (change) => {
    const user = userEvent.setup();
    let finish!: (response: Response) => void;
    let started = false;
    const fetchMock = api((url, init) => {
      if (url === "/api/v1/items/browse") {
        const body = JSON.parse(init.body as string);
        if (body.scope === "personal" || body.query === "Home") {
          if (!started) {
            started = true;
            return new Promise<Response>((resolve) => { finish = resolve; });
          }
          return json({ items: [second], next_cursor: null });
        }
      }
    });
    render(<VaultPage />); await screen.findByText(meta.title);
    await user.click(screen.getByLabelText(`选择 ${meta.title}`));
    if (change === "filter") await user.selectOptions(screen.getByLabelText("按保险库筛选"), "personal");
    else {
      fireEvent.change(screen.getByLabelText("搜索条目"), { target: { value: "Home" } });
      // Selection is blocked even during the search debounce interval.
      expect(screen.getByLabelText(`选择 ${meta.title}`)).toBeDisabled();
      expect(screen.getByRole("button", { name: "批量移入回收站" })).toBeDisabled();
    }
    await waitFor(() => expect(started).toBe(true));
    expect(screen.getByLabelText(`选择 ${meta.title}`)).toBeDisabled();
    expect(screen.getByLabelText("选择已加载的全部条目")).toBeDisabled();
    fireEvent.click(screen.getByLabelText(`选择 ${meta.title}`));
    const batchButton = screen.queryByRole("button", { name: "批量移至共享" });
    if (batchButton) { expect(batchButton).toBeDisabled(); fireEvent.click(batchButton); }
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await act(async () => finish(json({ items: [second], next_cursor: null })));
    expect(screen.queryByText(meta.title)).not.toBeInTheDocument();
    expect(screen.getByLabelText(`选择 ${second.title}`)).not.toBeChecked();
    await user.click(screen.getByLabelText(`选择 ${second.title}`));
    await user.click(screen.getByRole("button", { name: "批量移入回收站" }));
    await user.click(screen.getByRole("button", { name: "确认执行" }));
    await screen.findByText("完成 1 项。");
    expect(fetchMock.mock.calls.filter(([, init]) => init.method === "DELETE").map(([url]) => url)).toEqual(["/api/v1/items/two"]);
    expect(fetchMock.mock.calls.filter(([url]) => url === "/api/v1/items/one")).toHaveLength(0);
  });

  it("keeps selection while loading more and retries failed items from later pages", async () => {
    const user = userEvent.setup();
    let finish!: (response: Response) => void;
    let failures = 0;
    const fetchMock = api((url, init) => {
      if (url === "/api/v1/items/browse") {
        if (JSON.parse(init.body as string).cursor) return new Promise<Response>((resolve) => { finish = resolve; });
        return json({ items: [meta], next_cursor: "page-two" });
      }
      if (url === "/api/v1/items/two" && init.method === "DELETE" && failures++ === 0) return json({ code: "DATABASE_BUSY", message: "请稍后重试" }, 503);
    });
    render(<VaultPage />); await screen.findByText(meta.title);
    await user.click(screen.getByLabelText(`选择 ${meta.title}`));
    await user.click(screen.getByRole("button", { name: "加载更多" }));
    expect(screen.getByLabelText(`选择 ${meta.title}`)).toBeChecked();
    await act(async () => finish(json({ items: [second], next_cursor: null })));
    expect(screen.getByLabelText(`选择 ${meta.title}`)).toBeChecked();
    await user.click(screen.getByLabelText(`选择 ${second.title}`));
    await user.click(screen.getByRole("button", { name: "批量移入回收站" }));
    await user.click(screen.getByRole("button", { name: "确认执行" }));
    await screen.findByText(/完成 1 项，失败 1 项/);
    await waitFor(() => expect(screen.getByRole("button", { name: "批量移入回收站" })).toBeEnabled());
    expect(screen.getByText("已选 1 项")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "批量移入回收站" }));
    await user.click(screen.getByRole("button", { name: "确认执行" }));
    await screen.findByText("完成 1 项。");
    expect(fetchMock.mock.calls.filter(([, init]) => init.method === "DELETE").map(([url]) => url)).toEqual(["/api/v1/items/one", "/api/v1/items/two", "/api/v1/items/two"]);
    expect(fetchMock.mock.calls.filter(([url]) => url === "/api/v1/items/browse/tags")).toHaveLength(3); // initial + two mutations, never pagination
  });

  it("adds tags without losing old tags and rejects a stale selected revision", async () => {
    const user = userEvent.setup(); const succeeded = vi.fn();
    const fetchMock = api((url, init) => {
      if (url.endsWith("/two") && init.method === "GET") return json({ ...detail, ...second, revision: 2 });
    });
    render(<BulkActions items={[meta, second]} csrfToken={csrf} canManage={() => true} onSucceeded={succeeded} onChanged={() => {}} onBusyChange={() => {}} onClear={() => {}} />);
    await user.click(screen.getByRole("button", { name: "批量添加标签" }));
    await user.type(screen.getByLabelText("添加标签（逗号分隔）"), "work, new, new");
    await user.click(screen.getByRole("button", { name: "确认执行" }));
    await screen.findByText(/完成 1 项，失败 1 项/);
    const updates = fetchMock.mock.calls.filter(([, init]) => init.method === "PUT");
    expect(updates).toHaveLength(1);
    expect(JSON.parse(updates[0][1].body as string)).toMatchObject({ revision: 1, tags: ["work", "bank", "new"], payload: detail.payload });
    expect(succeeded).toHaveBeenCalledWith("one");
    expect(screen.getByText(/Home account：条目已更新/)).toBeInTheDocument();
  });

  it("reuses the exact create request and idempotency key after an uncertain network failure", async () => {
    const user = userEvent.setup(); let creates = 0;
    const fetchMock = api((url, init) => {
      if (url === "/api/v1/items" && init.method === "POST") {
        if (++creates === 1) throw new TypeError("network interrupted");
        return json({ ...detail, id: "copy" }, 201);
      }
    });
    render(<BulkActions items={[meta]} csrfToken={csrf} canManage={() => true} onSucceeded={() => {}} onChanged={() => {}} onBusyChange={() => {}} onClear={() => {}} />);
    for (let i = 0; i < 2; i++) {
      await user.click(screen.getByRole("button", { name: "复制为个人条目" }));
      await user.click(screen.getByRole("button", { name: "确认执行" }));
      await screen.findByText(i === 0 ? /完成 0 项，失败 1 项/ : "完成 1 项。");
    }
    const calls = fetchMock.mock.calls.filter(([url]) => url === "/api/v1/items");
    expect(calls).toHaveLength(2);
    expect(calls[0][1].body).toBe(calls[1][1].body);
    expect((calls[0][1].headers as Record<string, string>)["Idempotency-Key"]).toBe((calls[1][1].headers as Record<string, string>)["Idempotency-Key"]);
    expect(fetchMock.mock.calls.filter(([url]) => url === "/api/v1/items/one")).toHaveLength(1);
  });

  it("allows copying a read-only shared item into an editable personal draft", async () => {
    const user = userEvent.setup();
    const shared = { ...detail, vault_scope: "shared", owner_id: undefined, creator_id: "bob", creator_name: "bob" };
    const fetchMock = api((url) => url === "/api/v1/items/one" ? json(shared) : undefined);
    render(<VaultPage />); await user.click(await screen.findByRole("button", { name: /Work account/ }));
    await user.click(await screen.findByRole("button", { name: "复制为新条目" }));
    const form = await screen.findByRole("form", { name: "新建条目" });
    expect(within(form).getByLabelText("名称")).toHaveValue("Work account（副本）");
    expect(within(form).getByLabelText("密码")).toHaveValue("SYNSECRET-password");
    expect(within(form).getByLabelText("个人保险库")).toBeChecked();
    expect(fetchMock.mock.calls.filter(([url]) => url === "/api/v1/items")).toHaveLength(0);
    expect(window.location.pathname).toBe("/vault");
  });

  it("disables bulk writes when selection contains another member's shared item", () => {
    api();
    render(<BulkActions items={[{ ...meta, vault_scope: "shared", creator_id: "bob" }]} csrfToken={csrf} canManage={() => false} onSucceeded={() => {}} onChanged={() => {}} onBusyChange={() => {}} onClear={() => {}} />);
    expect(screen.getByRole("button", { name: "批量移入回收站" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "批量移至共享" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "批量添加标签" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "复制为个人条目" })).toBeEnabled();
  });

  it("filters health findings and opens the chosen item", async () => {
    const user = userEvent.setup();
    api((url) => url === "/api/v1/items/health" ? json({ weak: 1, reused: 1, expired: 0, items: [{ item_id: "one", title: meta.title, reasons: ["weak"] }, { item_id: "two", title: second.title, reasons: ["reused"] }] }) : undefined);
    render(<VaultPage />); await user.click(await screen.findByRole("button", { name: "查看问题条目" }));
    const dialog = screen.getByRole("dialog", { name: "密码健康" });
    await user.click(within(dialog).getByRole("button", { name: "重复密码 · 1" }));
    expect(within(dialog).queryByText(meta.title)).not.toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "查看条目" }));
    await screen.findByRole("dialog", { name: second.title });
    expect(window.location.pathname).toBe("/vault/two");
  });

  it("fetches a password only on explicit quick copy and audits without displaying it", async () => {
    const user = userEvent.setup(); const fetchMock = api(); const writeText = vi.fn(async () => {});
    render(<VaultPage />); await screen.findByText(meta.title);
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText, readText: vi.fn(async () => "") } });
    expect(fetchMock.mock.calls.filter(([url]) => url === "/api/v1/items/one")).toHaveLength(0);
    await user.click(within(screen.getByRole("group", { name: "Work account 的快捷复制" })).getByRole("button", { name: "复制密码" }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith("SYNSECRET-password"));
    expect(screen.queryByText("SYNSECRET-password")).not.toBeInTheDocument();
    const audit = fetchMock.mock.calls.find(([url]) => url.endsWith("/copy"));
    expect(JSON.parse(audit![1].body as string)).toEqual({ field: "password" });
  });

  it("ignores new-item shortcuts while typing and focuses search with the slash shortcut", async () => {
    api(); render(<VaultPage />); await screen.findByText(meta.title);
    fireEvent.keyDown(window, { key: "/" });
    expect(screen.getByLabelText("搜索条目")).toHaveFocus();
    fireEvent.keyDown(screen.getByLabelText("搜索条目"), { key: "n" });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    fireEvent.keyDown(window, { key: "n" });
    expect(await screen.findByRole("form", { name: "新建条目" })).toBeInTheDocument();
  });
});

describe("Quick copy lifetime", () => {
  it.each([false, true])("cleans a late successful write after unmount without erasing newer content (%s)", async (changed) => {
    vi.useFakeTimers();
    let finishWrite!: () => void;
    let finishAudit!: (response: Response) => void;
    const fetchMock = api((url) => url.endsWith("/copy") ? new Promise<Response>((resolve) => { finishAudit = resolve; }) : undefined);
    const writeText = vi.fn().mockImplementationOnce(() => new Promise<void>((resolve) => { finishWrite = resolve; })).mockResolvedValue(undefined);
    const readText = vi.fn(async () => changed ? "new clipboard content" : "SYNSECRET-password");
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText, readText } });
    const { unmount, container } = render(<QuickCopy itemId="one" title={meta.title} csrfToken={csrf} />);
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "复制密码" })));
    expect(writeText).toHaveBeenCalledWith("SYNSECRET-password");
    unmount();
    await act(async () => finishWrite());
    const audit = fetchMock.mock.calls.find(([url]) => url.endsWith("/copy"))!;
    expect(JSON.parse(audit[1].body as string)).toEqual({ field: "password" });
    expect((audit[1].signal as AbortSignal).aborted).toBe(false);
    expect(container).toBeEmptyDOMElement();
    await act(async () => vi.advanceTimersByTimeAsync(30_000));
    expect(readText).toHaveBeenCalledTimes(1);
    if (changed) expect(writeText).toHaveBeenCalledTimes(1);
    else expect(writeText).toHaveBeenLastCalledWith("");
    await act(async () => finishAudit(json({}, 204)));
  });

  it("does not cancel a successful copy audit on navigation, but retains global session cancellation", async () => {
    let finishAudit!: (response: Response) => void;
    const fetchMock = api((url) => url.endsWith("/copy") ? new Promise<Response>((resolve) => { finishAudit = resolve; }) : undefined);
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: vi.fn(async () => {}), readText: vi.fn(async () => "") } });
    const { unmount } = render(<QuickCopy itemId="one" title={meta.title} csrfToken={csrf} />);
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "复制密码" })));
    const audit = fetchMock.mock.calls.find(([url]) => url.endsWith("/copy"))!;
    unmount();
    expect((audit[1].signal as AbortSignal).aborted).toBe(false);
    abortInFlightRequests();
    expect((audit[1].signal as AbortSignal).aborted).toBe(true);
    await act(async () => finishAudit(json({}, 204)));
  });

  it.each([false, true])("does not audit or schedule cleanup for a failed write (unmounted: %s)", async (unmounted) => {
    vi.useFakeTimers();
    let failWrite!: (error: Error) => void;
    const fetchMock = api();
    const writeText = vi.fn(() => new Promise<void>((_, reject) => { failWrite = reject; }));
    const readText = vi.fn();
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText, readText } });
    const { unmount } = render(<QuickCopy itemId="one" title={meta.title} csrfToken={csrf} />);
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "复制密码" })));
    if (unmounted) unmount();
    await act(async () => failWrite(new DOMException("Denied", "NotAllowedError")));
    if (!unmounted) {
      expect(screen.getByRole("status")).toHaveTextContent("复制失败");
      expect(screen.getByRole("button", { name: "复制密码" })).toBeEnabled();
    }
    await act(async () => vi.advanceTimersByTimeAsync(30_000));
    expect(readText).not.toHaveBeenCalled();
    expect(fetchMock.mock.calls.filter(([url]) => url.endsWith("/copy"))).toHaveLength(0);
  });
});

describe("Editor password generation", () => {
  it("generates with chosen options, blocks saving in flight, and preserves other draft fields", async () => {
    const user = userEvent.setup(); let finish!: (response: Response) => void;
    const fetchMock = api((url) => url === "/api/v1/generators/password" ? new Promise<Response>((resolve) => { finish = resolve; }) : undefined);
    render(<ItemEditor initial={detail} csrfToken={csrf} onSaved={() => {}} onCancel={() => {}} />);
    await user.click(screen.getByRole("button", { name: "生成密码" }));
    await user.clear(screen.getByLabelText("密码长度")); await user.type(screen.getByLabelText("密码长度"), "32");
    await user.click(screen.getByLabelText("符号"));
    await user.click(screen.getByRole("button", { name: "生成并填入" }));
    expect(screen.getByRole("button", { name: "保存修改" })).toBeDisabled();
    await act(async () => finish(json({ value: "random-generated-password-32" })));
    expect(screen.getByLabelText("密码")).toHaveValue("random-generated-password-32");
    expect(screen.getByLabelText("备注")).toHaveValue("Keep me");
    expect(screen.getByRole("button", { name: "保存修改" })).toBeEnabled();
    const generate = fetchMock.mock.calls.find(([url]) => url.endsWith("/generators/password"));
    expect(JSON.parse(generate![1].body as string)).toMatchObject({ length: 32, symbols: false });
    expect(screen.getByText(/密码强度：/)).toBeInTheDocument();
  });

  it("aborts generator requests on unmount so a late result cannot fill a new editor", async () => {
    const user = userEvent.setup(); let finish!: (response: Response) => void; const changed = vi.fn();
    const fetchMock = api((url) => url.endsWith("/generators/password") ? new Promise<Response>((resolve) => { finish = resolve; }) : undefined);
    const { unmount } = render(<ItemEditor initial={detail} csrfToken={csrf} onSaved={changed} onCancel={() => {}} />);
    await user.click(screen.getByRole("button", { name: "生成密码" })); await user.click(screen.getByRole("button", { name: "生成并填入" }));
    unmount();
    const call = fetchMock.mock.calls.find(([url]) => url.endsWith("/generators/password"));
    expect((call![1].signal as AbortSignal).aborted).toBe(true);
    await act(async () => finish(json({ value: "late-secret" })));
    expect(changed).not.toHaveBeenCalled();
  });
});
