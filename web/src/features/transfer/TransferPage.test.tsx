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
	it("shows download progress after the export response starts", async () => {
		const user = userEvent.setup();
		sessionStore.set({ principal, csrfToken: "csrf" });
		let finishDownload: (() => void) | undefined;
		const fetchMock = vi.fn(async (url: string | URL) => {
			if (!String(url).includes("export")) return jsonResponse(200, {});
			const body = new ReadableStream<Uint8Array>({
				start(controller) {
					controller.enqueue(new Uint8Array([1, 2, 3, 4]));
					finishDownload = () => {
						controller.enqueue(new Uint8Array([5, 6, 7, 8]));
						controller.close();
					};
				},
			});
			return new Response(body, {
				status: 200,
				headers: { "Content-Length": "8", "Content-Type": "application/x-7z-compressed" },
			});
		});
		vi.stubGlobal("fetch", fetchMock);
		vi.stubGlobal("URL", { ...URL, createObjectURL: vi.fn(() => "blob:export"), revokeObjectURL: vi.fn() });
		vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);
		render(<TransferPage />);

		await user.type(screen.getByLabelText("归档口令"), "archive-passphrase");
		await user.type(screen.getByLabelText("确认归档口令"), "archive-passphrase");
		await user.click(screen.getByRole("button", { name: "下载加密归档" }));

		await waitFor(() => expect(screen.getByRole("button", { name: "正在下载… 50%" })).toBeInTheDocument());
		finishDownload?.();
		await screen.findByText("归档已下载。请妥善保管归档口令——没有口令将无法恢复。");
	});

	it("keeps the blob URL alive after starting the local download", async () => {
		const user = userEvent.setup();
		sessionStore.set({ principal, csrfToken: "csrf" });
		const fetchMock = vi.fn(async () =>
			new Response(new Blob(["archive"]), {
				status: 200,
				headers: { "Content-Type": "application/x-7z-compressed" },
			}),
		);
		const revokeObjectURL = vi.fn();
		vi.stubGlobal("fetch", fetchMock);
		vi.stubGlobal("URL", { ...URL, createObjectURL: vi.fn(() => "blob:export"), revokeObjectURL });
		vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);
		render(<TransferPage />);

		await user.type(screen.getByLabelText("归档口令"), "archive-passphrase");
		await user.type(screen.getByLabelText("确认归档口令"), "archive-passphrase");
		await user.click(screen.getByRole("button", { name: "下载加密归档" }));

		await screen.findByText("归档已下载。请妥善保管归档口令——没有口令将无法恢复。");
		expect(revokeObjectURL).not.toHaveBeenCalled();
	});

	it("blocks export when the archive passphrase is shorter than the server minimum", async () => {
    const user = userEvent.setup();
    sessionStore.set({ principal, csrfToken: "csrf" });
    const fetchMock = vi.fn(async () => jsonResponse(200, {}));
    vi.stubGlobal("fetch", fetchMock);
    render(<TransferPage />);

    await user.type(screen.getByLabelText("归档口令"), "short");
    await user.type(screen.getByLabelText("确认归档口令"), "short");
    await user.click(screen.getByRole("button", { name: "下载加密归档" }));

    expect(await screen.findByText("归档口令长度需为 12–1024 字节。"))
      .toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });

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

  it("previews and confirms a Bitwarden JSON file", async () => {
    const user = userEvent.setup();
    sessionStore.set({ principal, csrfToken: "csrf" });
    const fetchMock = vi.fn(async (url: string | URL, _init?: RequestInit) => {
      void _init;
      if (String(url).includes("bitwarden/preview")) {
        return jsonResponse(200, {
          preview_token: "bw-preview",
          counts: { login: 1 },
          conflicts: 0,
          missing_references: [],
        });
      }
      return jsonResponse(200, { imported_count: 1 });
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<TransferPage />);

    await user.selectOptions(screen.getByLabelText("导入格式"), "bitwarden");
    await user.upload(
      screen.getByLabelText("Bitwarden JSON 文件"),
      new File(["{}"], "data.json", { type: "application/json" }),
    );
    await user.click(screen.getByRole("button", { name: "预览 Bitwarden 导入" }));
    await screen.findByRole("region", { name: "导入预览" });

    const init = fetchMock.mock.calls[0][1] as RequestInit;
    expect(init.body).toBeInstanceOf(FormData);
    expect((init.body as FormData).get("file")).toBeInstanceOf(File);
    expect((init.body as FormData).get("passphrase")).toBeNull();
    await user.click(screen.getByRole("button", { name: "确认导入" }));
    await waitFor(() =>
      expect(fetchMock.mock.calls[1][0]).toBe("/api/v1/transfer/import/confirm"),
    );
  });

  it("cancels the previous preview when the selected file changes", async () => {
    const user = userEvent.setup();
    sessionStore.set({ principal, csrfToken: "csrf" });
    const fetchMock = vi.fn(async (url: string | URL) => {
      if (String(url).includes("/import/preview")) {
        return jsonResponse(200, {
          preview_token: "preview-1",
          counts: { login: 1 },
          conflicts: 0,
          missing_references: [],
        });
      }
      return jsonResponse(204, {});
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<TransferPage />);

    const fileInput = screen.getByLabelText("归档文件（.7z）");
    await user.upload(fileInput, new File(["archive-1"], "one.7z"));
    await user.click(screen.getByRole("button", { name: "预览导入" }));
    await screen.findByRole("region", { name: "导入预览" });
    await user.upload(fileInput, new File(["archive-2"], "two.7z"));

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(fetchMock.mock.calls[1][0]).toBe("/api/v1/transfer/import/cancel");
    expect(screen.queryByRole("region", { name: "导入预览" })).not.toBeInTheDocument();
  });
});
