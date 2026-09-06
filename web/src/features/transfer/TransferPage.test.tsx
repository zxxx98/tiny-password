import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { TransferPage } from "./TransferPage";
import { SESSION_EXPIRED_EVENT, sessionStore } from "../../app/session";

function jsonResponse(status: number, body: unknown) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const principal = {
  user_id: "u1",
  username: "alice",
  role: "member" as const,
  must_change_password: false,
  idle_timeout_minutes: 15,
};

afterEach(() => {
  vi.unstubAllGlobals();
  sessionStore.clear();
});

describe("TransferPage lifecycle", () => {
  it("clears import secrets and preview after a preview 401", async () => {
    const user = userEvent.setup();
    sessionStore.set({ principal, csrfToken: "csrf" });
    const fetchMock = vi.fn(async () => jsonResponse(401, { code: "UNAUTHORIZED", message: "expired", request_id: "r" }));
    vi.stubGlobal("fetch", fetchMock);
    render(<TransferPage />);
    await user.upload(screen.getByLabelText("归档文件（.7z）"), new File(["archive"], "backup.7z"));
    await user.type(screen.getByLabelText("归档口令（导入）"), "archive-passphrase");
    await user.click(screen.getByRole("button", { name: "预览导入" }));

    await waitFor(() => expect(sessionStore.get().principal).toBeNull());
    expect(screen.getByLabelText("归档口令（导入）")).toHaveValue("");
    expect(screen.queryByRole("region", { name: "导入预览" })).not.toBeInTheDocument();
    expect(screen.getByLabelText("归档文件（.7z）")).toHaveValue("");
  });

  it("does not download a late export response after session expiry", async () => {
    const user = userEvent.setup();
    sessionStore.set({ principal, csrfToken: "csrf" });
    let resolveExport: ((response: Response) => void) | undefined;
    const fetchMock = vi.fn((_url: string | URL, init?: RequestInit) => {
      if (String(_url).includes("export")) {
        return new Promise<Response>((resolve, reject) => {
          resolveExport = resolve;
          init?.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
        });
      }
      return Promise.resolve(jsonResponse(200, {}));
    });
    vi.stubGlobal("fetch", fetchMock);
    const createObjectURL = vi.fn(() => "blob:late");
    const click = vi.fn();
    vi.stubGlobal("URL", { ...URL, createObjectURL, revokeObjectURL: vi.fn() });
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(click);
    render(<TransferPage />);
    await user.type(screen.getByLabelText("归档口令"), "archive-passphrase");
    await user.type(screen.getByLabelText("确认归档口令"), "archive-passphrase");
    await user.click(screen.getByRole("button", { name: "下载加密归档" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalled());

    sessionStore.clear();
    window.dispatchEvent(new CustomEvent(SESSION_EXPIRED_EVENT));
    resolveExport?.(new Response(new Blob(["archive"]), { status: 200 }));
    await waitFor(() => expect(screen.getByLabelText("归档口令")).toHaveValue(""));
    expect(createObjectURL).not.toHaveBeenCalled();
    expect(click).not.toHaveBeenCalled();
  });

  it("cancels a preview token when the page unmounts", async () => {
    const user = userEvent.setup();
    sessionStore.set({ principal, csrfToken: "csrf" });
    const fetchMock = vi.fn(async (url: string | URL, init?: RequestInit) => {
      void init;
      if (String(url).includes("preview")) {
        return jsonResponse(200, { preview_token: "preview-1", counts: {}, conflicts: 0, missing_references: [] });
      }
      return jsonResponse(204, {});
    });
    vi.stubGlobal("fetch", fetchMock);
    const view = render(<TransferPage />);
    await user.upload(screen.getByLabelText("归档文件（.7z）"), new File(["archive"], "backup.7z"));
    await user.type(screen.getByLabelText("归档口令（导入）"), "archive-passphrase");
    await user.click(screen.getByRole("button", { name: "预览导入" }));
    await screen.findByRole("region", { name: "导入预览" });
    view.unmount();

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(fetchMock.mock.calls[1][0]).toBe("/api/v1/transfer/import/cancel");
    expect(JSON.parse((fetchMock.mock.calls[1][1] as RequestInit).body as string)).toEqual({ preview_token: "preview-1" });
  });

  it("clears the session without recursively broadcasting when teardown cancellation gets a 401", async () => {
    const user = userEvent.setup();
    sessionStore.set({ principal, csrfToken: "csrf" });
    const fetchMock = vi.fn(async (url: string | URL, init?: RequestInit) => {
      void init;
      if (String(url).includes("preview")) {
        return jsonResponse(200, { preview_token: "preview-1", counts: {}, conflicts: 0, missing_references: [] });
      }
      return jsonResponse(401, { code: "UNAUTHORIZED", message: "expired", request_id: "r-cleanup" });
    });
    vi.stubGlobal("fetch", fetchMock);
    const expired = vi.fn();
    window.addEventListener(SESSION_EXPIRED_EVENT, expired);
    const view = render(<TransferPage />);
    await user.upload(screen.getByLabelText("归档文件（.7z）"), new File(["archive"], "backup.7z"));
    await user.type(screen.getByLabelText("归档口令（导入）"), "archive-passphrase");
    await user.click(screen.getByRole("button", { name: "预览导入" }));
    await screen.findByRole("region", { name: "导入预览" });
    view.unmount();

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(sessionStore.get().principal).toBeNull();
    expect(expired).not.toHaveBeenCalled();
    window.removeEventListener(SESSION_EXPIRED_EVENT, expired);
  });
});
