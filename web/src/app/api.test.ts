import { afterEach, describe, expect, it, vi } from "vitest";
import { abortInFlightRequests, request, requestBlob } from "./api";
import { SESSION_EXPIRED_EVENT, sessionStore } from "./session";

function jsonResponse(status: number, body: unknown) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

afterEach(() => {
  vi.unstubAllGlobals();
  sessionStore.clear();
});

describe("session-aware request bodies", () => {
  it("passes FormData through without adding a JSON content type", async () => {
    const fetchMock = vi.fn(async (_url: string | URL, init?: RequestInit) => {
      void init;
      return jsonResponse(200, { ok: true });
    });
    vi.stubGlobal("fetch", fetchMock);
    const form = new FormData();
    form.append("archive", new File(["archive"], "backup.7z"));

    await expect(request<{ ok: boolean }>("POST", "/api/v1/transfer/import/preview", form)).resolves.toEqual({ ok: true });

    const init = fetchMock.mock.calls[0][1] as RequestInit;
    expect(init.body).toBe(form);
    expect(new Headers(init.headers).has("Content-Type")).toBe(false);
  });

  it("clears the session and aborts a binary request when another request receives 401", async () => {
    sessionStore.set({
      principal: {
        user_id: "u1",
        username: "alice",
        role: "member",
        must_change_password: false,
        idle_timeout_minutes: 15,
      },
      csrfToken: "csrf",
    });
    let rejectSlow: ((reason?: unknown) => void) | undefined;
    const fetchMock = vi.fn((url: string | URL, init?: RequestInit): Promise<Response> => {
      if (String(url).includes("slow")) {
        return new Promise<Response>((_resolve, reject) => {
          rejectSlow = reject;
          init?.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
        });
      }
      return Promise.resolve(jsonResponse(401, { code: "UNAUTHORIZED", message: "expired", request_id: "r-1" }));
    });
    vi.stubGlobal("fetch", fetchMock);
    const expired = vi.fn();
    const abortOnExpired = () => abortInFlightRequests();
    window.addEventListener(SESSION_EXPIRED_EVENT, expired);
    window.addEventListener(SESSION_EXPIRED_EVENT, abortOnExpired);
    const slow = requestBlob("POST", "/api/v1/slow-export", undefined, { csrfToken: "csrf" }).catch(() => undefined);
    await expect(request("GET", "/api/v1/trigger-expiry")).rejects.toMatchObject({ status: 401 });
    await slow;
    expect(sessionStore.get().principal).toBeNull();
    expect(expired).toHaveBeenCalledTimes(1);
    expect((fetchMock.mock.calls[0][1] as RequestInit).signal?.aborted).toBe(true);
    rejectSlow?.(new DOMException("aborted", "AbortError"));
    window.removeEventListener(SESSION_EXPIRED_EVENT, expired);
    window.removeEventListener(SESSION_EXPIRED_EVENT, abortOnExpired);
  });

  it("still clears the session when a cleanup 401 suppresses the expiry event", async () => {
    sessionStore.set({
      principal: {
        user_id: "u1",
        username: "alice",
        role: "member",
        must_change_password: false,
        idle_timeout_minutes: 15,
      },
      csrfToken: "csrf",
    });
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse(401, { code: "UNAUTHORIZED", message: "expired", request_id: "r-2" })));
    const expired = vi.fn();
    window.addEventListener(SESSION_EXPIRED_EVENT, expired);

    await expect(
      request("POST", "/api/v1/transfer/import/cancel", { preview_token: "preview-1" }, {
        csrfToken: "csrf",
        suppressAuthFailure: true,
      }),
    ).rejects.toMatchObject({ status: 401 });

    expect(sessionStore.get().principal).toBeNull();
    expect(expired).not.toHaveBeenCalled();
    window.removeEventListener(SESSION_EXPIRED_EVENT, expired);
  });
});
