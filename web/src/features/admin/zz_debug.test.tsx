import { render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi, afterEach } from "vitest";
import { UsersPage } from "./UsersPage";
import { sessionStore } from "../../app/session";

afterEach(() => { vi.unstubAllGlobals(); sessionStore.clear(); });

const member = {
  id: "11111111-2222-7333-8444-555555555555",
  username: "bob",
  role: "member",
  status: "active",
  created_at: "2026-09-05T10:00:00.000000000Z",
};

function jsonResponse(status: number, body: unknown) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

describe("debug3", () => {
  it("queue-based stub", async () => {
    sessionStore.set({ principal: { user_id: "a", username: "A", role: "admin", must_change_password: false, idle_timeout_minutes: 15 }, csrfToken: "csrf" });
    const queue = [{ status: 200, body: { items: [member], next_cursor: null } }];
    vi.stubGlobal("fetch", vi.fn(async (_url: string | URL, init?: RequestInit) => {
      void init;
      const step = queue.shift() ?? { status: 200, body: { items: [], next_cursor: null } };
      console.log("FETCH", String(_url), "STATUS", step.status);
      if (step.status === 204) return new Response(null, { status: 204 });
      return jsonResponse(step.status, step.body ?? {});
    }));
    render(<UsersPage />);
    try {
      await waitFor(() => expect(screen.getByText(/bob/)).toBeInTheDocument(), { timeout: 3000 });
      console.log("BOB FOUND");
    } catch {
      console.log("WAIT FAIL; body snippet:", document.body.textContent?.slice(0, 200));
    }
  });
});
