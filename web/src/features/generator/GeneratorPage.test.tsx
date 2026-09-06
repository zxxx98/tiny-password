import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { GeneratorPage } from "./GeneratorPage";
import { consumeNavigationState } from "../../app/router";
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
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  sessionStore.clear();
  window.history.replaceState({}, "", "/generator");
});

describe("GeneratorPage", () => {
  it("masks generated secrets by default and reveals them only on request", async () => {
    const user = userEvent.setup();
    sessionStore.set({ principal, csrfToken: "csrf" });
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse(200, { value: "GENERATED-PASSWORD" })));
    render(<GeneratorPage />);
    await user.click(screen.getByRole("button", { name: "生成密码" }));
    await screen.findByRole("button", { name: "显示密码" });
    expect(screen.getByLabelText("生成的密码")).not.toHaveTextContent("GENERATED-PASSWORD");
    await user.click(screen.getByRole("button", { name: "显示密码" }));
    expect(screen.getByLabelText("生成的密码")).toHaveTextContent("GENERATED-PASSWORD");
    await user.click(screen.getByRole("button", { name: "隐藏密码" }));
    expect(screen.getByLabelText("生成的密码")).not.toHaveTextContent("GENERATED-PASSWORD");
  });

  it("saves SSH passphrase and comment from generation A after inputs are edited to B", async () => {
    const user = userEvent.setup();
    sessionStore.set({ principal, csrfToken: "csrf" });
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse(200, {
        algorithm: "ed25519",
        public_key: "ssh-ed25519 PUBLIC",
        private_key: "PRIVATE-A",
        fingerprint: "SHA256:fingerprint",
      }))
      .mockResolvedValueOnce(jsonResponse(201, { id: "ssh-1" }));
    vi.stubGlobal("fetch", fetchMock);
    render(<GeneratorPage />);
    await user.type(screen.getByLabelText("私钥口令（可选）"), "passphrase-A");
    await user.type(screen.getByLabelText("注释（可选）"), "comment-A");
    await user.click(screen.getByRole("button", { name: "生成密钥" }));
    await screen.findByRole("button", { name: "保存为 SSH 条目" });
    await user.selectOptions(screen.getByLabelText("算法"), "rsa4096");
    await user.clear(screen.getByLabelText("私钥口令（可选）"));
    await user.type(screen.getByLabelText("私钥口令（可选）"), "passphrase-B");
    await user.clear(screen.getByLabelText("注释（可选）"));
    await user.type(screen.getByLabelText("注释（可选）"), "comment-B");
    await user.click(screen.getByRole("button", { name: "保存为 SSH 条目" }));

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    const payload = JSON.parse((fetchMock.mock.calls[1][1] as RequestInit).body as string).payload;
    expect(payload.algorithm).toBe("ed25519");
    expect(payload.key_passphrase).toBe("passphrase-A");
    expect(payload.comment).toBe("comment-A");
  });

  it("keeps generated passphrases and private keys out of the DOM until revealed", async () => {
    const user = userEvent.setup();
    sessionStore.set({ principal, csrfToken: "csrf" });
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse(200, { value: "GENERATED-PASSPHRASE" }))
      .mockResolvedValueOnce(jsonResponse(200, {
        algorithm: "ed25519",
        public_key: "ssh-ed25519 PUBLIC",
        private_key: "PRIVATE-KEY-SECRET",
        fingerprint: "SHA256:fingerprint",
      }));
    vi.stubGlobal("fetch", fetchMock);
    render(<GeneratorPage />);

    await user.click(screen.getByRole("button", { name: "生成口令" }));
    await screen.findByRole("button", { name: "显示口令" });
    expect(screen.getByLabelText("生成的口令")).not.toHaveTextContent("GENERATED-PASSPHRASE");

    await user.click(screen.getByRole("button", { name: "生成密钥" }));
    await screen.findByRole("button", { name: "显示私钥" });
    expect(screen.getByLabelText("生成的私钥")).not.toHaveTextContent("PRIVATE-KEY-SECRET");
    await user.click(screen.getByRole("button", { name: "显示私钥" }));
    expect(screen.getByLabelText("生成的私钥")).toHaveTextContent("PRIVATE-KEY-SECRET");
  });

  it("clears generated values when the page unmounts", async () => {
    const user = userEvent.setup();
    sessionStore.set({ principal, csrfToken: "csrf" });
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse(200, { value: "GENERATED-PASSWORD" })));
    const view = render(<GeneratorPage />);
    await user.click(screen.getByRole("button", { name: "生成密码" }));
    await screen.findByRole("button", { name: "显示密码" });
    view.unmount();
    expect(screen.queryByLabelText("生成的密码")).not.toBeInTheDocument();
  });

  it("hands a generated password to a new personal login editor in memory", async () => {
    const user = userEvent.setup();
    sessionStore.set({ principal, csrfToken: "csrf" });
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse(200, { value: "HANDOFF-PASSWORD" })));
    render(<GeneratorPage />);
    await user.click(screen.getByRole("button", { name: "生成密码" }));
    await user.click(await screen.findByRole("button", { name: "用于新建登录密码" }));

    expect(window.location.pathname).toBe("/vault");
    expect(consumeNavigationState()).toEqual({ kind: "new-login", password: "HANDOFF-PASSWORD" });
  });

  it("clears generator state and ignores a late result after session expiry", async () => {
    const user = userEvent.setup();
    sessionStore.set({ principal, csrfToken: "csrf" });
    vi.stubGlobal(
      "fetch",
      vi.fn((_url: string | URL, init?: RequestInit) =>
        new Promise<Response>((_resolve, reject) => {
          init?.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
        }),
      ),
    );
    render(<GeneratorPage />);
    await user.click(screen.getByRole("button", { name: "生成密码" }));
    window.dispatchEvent(new CustomEvent(SESSION_EXPIRED_EVENT));
    await waitFor(() => expect(screen.queryByLabelText("生成的密码")).not.toBeInTheDocument());
  });
});
