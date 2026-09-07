import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AuditPage } from "./AuditPage";
import { BackupsPage } from "./BackupsPage";
import { SettingsPage } from "./SettingsPage";
import { sessionStore } from "../../app/session";

function jsonResponse(status: number, body: unknown) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

// fetchCalls records method+path pairs so tests can assert the requests
// each page issues.
function stubFetch(steps: Array<{ status: number; body?: unknown }>) {
  const queue = [...steps];
  const calls: Array<{ method: string; url: string; body: unknown }> = [];
  const fetchMock = vi.fn(async (url: string | URL, init?: RequestInit): Promise<Response> => {
    const step = queue.shift() ?? { status: 200, body: {} };
    calls.push({
      method: init?.method ?? "GET",
      url: String(url),
      body: init?.body ? JSON.parse(String(init.body)) : null,
    });
    return jsonResponse(step.status, step.body ?? {});
  });
  vi.stubGlobal("fetch", fetchMock);
  return { fetchMock, calls };
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

const jobsBody = {
  jobs: [
    {
      target: "local",
      enabled: true,
      schedule_time: "03:30",
      schedule_timezone: "UTC",
      retention_daily: 7,
      retention_weekly: 4,
      retention_monthly: 6,
      delivery_ready: true,
    },
    {
      target: "r2",
      enabled: false,
      schedule_time: "",
      schedule_timezone: "",
      retention_daily: 7,
      retention_weekly: 4,
      retention_monthly: 6,
      delivery_ready: false,
    },
  ],
};

const runsBody = {
  items: [
    {
      id: "run-1",
      target: "local",
      status: "succeeded",
      triggered_by: "manual",
      started_at: "2026-09-06T12:00:00Z",
      finished_at: "2026-09-06T12:01:00Z",
      size_bytes: 20480,
      error_code: null,
    },
    {
      id: "run-2",
      target: "r2",
      status: "failed",
      triggered_by: "scheduled",
      started_at: "2026-09-06T03:00:00Z",
      finished_at: "2026-09-06T03:00:10Z",
      size_bytes: null,
      error_code: "BACKUP_UPLOAD_FAILED",
    },
  ],
  next_cursor: null,
};

describe("BackupsPage", () => {
  it("renders per-target configuration and the independent run history", async () => {
    seedAdmin();
    stubFetch([
      { status: 200, body: jobsBody },
      { status: 200, body: runsBody },
    ]);
    render(<BackupsPage />);

    await waitFor(() => expect(screen.getByText("本地备份")).toBeInTheDocument());
    expect(screen.getByText("R2 备份")).toBeInTheDocument();
    // The R2 target is flagged as not delivery-ready.
    expect(screen.getByText("未配置交付")).toBeInTheDocument();
    // Both runs appear independently: a success and a failure with code.
    expect(screen.getByText(/成功/)).toBeInTheDocument();
    expect(screen.getByText(/BACKUP_UPLOAD_FAILED/)).toBeInTheDocument();
  });

  it("saves a job configuration and triggers a manual run", async () => {
    const user = userEvent.setup();
    seedAdmin();
    const { calls } = stubFetch([
      { status: 200, body: jobsBody }, // initial jobs
      { status: 200, body: runsBody }, // initial runs
      { status: 200, body: { updated: true } }, // save local
      { status: 200, body: jobsBody }, // reload jobs
      { status: 200, body: runsBody }, // reload runs
      { status: 202, body: { started: true } }, // manual run (local)
    ]);
    render(<BackupsPage />);
    await waitFor(() => expect(screen.getByText("本地备份")).toBeInTheDocument());

    await user.click(screen.getAllByRole("button", { name: "保存配置" })[0]);
    await waitFor(() =>
      expect(calls.some((c) => c.method === "PUT" && c.url.includes("/admin/backups/jobs"))).toBe(true),
    );
    const put = calls.find((c) => c.method === "PUT");
    expect(put?.body).toMatchObject({ target: "local", schedule_time: "03:30" });

    await user.click(screen.getAllByRole("button", { name: "立即执行" })[0]);
    await waitFor(() =>
      expect(calls.some((c) => c.method === "POST" && c.url.includes("/admin/backups/run"))).toBe(true),
    );
    const post = calls.find((c) => c.method === "POST" && c.url.includes("/admin/backups/run"));
    expect(post?.body).toEqual({ targets: ["local"] });
  });

  it("surfaces the busy and passphrase errors from the API", async () => {
    const user = userEvent.setup();
    seedAdmin();
    stubFetch([
      { status: 200, body: jobsBody },
      { status: 200, body: { items: [], next_cursor: null } },
      { status: 409, body: { code: "BACKUP_BUSY", message: "another backup is running" } },
    ]);
    render(<BackupsPage />);
    await waitFor(() => expect(screen.getByText("本地备份")).toBeInTheDocument());

    await user.click(screen.getAllByRole("button", { name: "立即执行" })[0]);
    await waitFor(() => expect(screen.getByText("已有备份在运行，请稍后再试。")).toBeInTheDocument());
  });

  it("disables run actions for targets without delivery configuration", async () => {
    seedAdmin();
    stubFetch([
      { status: 200, body: jobsBody },
      { status: 200, body: { items: [], next_cursor: null } },
    ]);
    render(<BackupsPage />);
    await waitFor(() => expect(screen.getByText("本地备份")).toBeInTheDocument());

    // The configured local target can run; the unconfigured R2 target and
    // the all-targets shortcut cannot.
    const runButtons = screen.getAllByRole("button", { name: "立即执行" });
    expect(runButtons[0]).toBeEnabled();
    expect(runButtons[1]).toBeDisabled();
    expect(screen.getByRole("button", { name: "立即备份全部目标" })).toBeDisabled();
  });
});

describe("AuditPage", () => {
  it("lists redacted events and applies the result filter", async () => {
    const user = userEvent.setup();
    seedAdmin();
    const { calls } = stubFetch([
      { status: 200, body: { items: [], next_cursor: null } },
      { status: 200, body: { items: [], next_cursor: null } },
    ]);
    render(<AuditPage />);
    await waitFor(() => expect(screen.getByRole("button", { name: "应用筛选" })).toBeInTheDocument());

    await user.selectOptions(screen.getByLabelText("结果筛选"), "failure");
    await user.click(screen.getByRole("button", { name: "应用筛选" }));
    await waitFor(() =>
      expect(calls.some((c) => c.url.includes("result=failure"))).toBe(true),
    );
  });

  it("sends UTC time bounds with the audit query", async () => {
    seedAdmin();
    const { calls } = stubFetch([
      { status: 200, body: { items: [], next_cursor: null } },
      { status: 200, body: { items: [], next_cursor: null } },
    ]);
    render(<AuditPage />);
    await waitFor(() => expect(screen.getByRole("button", { name: "应用筛选" })).toBeInTheDocument());

    fireEvent.change(screen.getByLabelText("起始时间"), { target: { value: "2026-09-02T00:00" } });
    fireEvent.change(screen.getByLabelText("结束时间"), { target: { value: "2026-09-04T00:00" } });
    await userEvent.setup().click(screen.getByRole("button", { name: "应用筛选" }));
    await waitFor(() => {
      const url = calls.at(-1)?.url ?? "";
      expect(url).toContain("from=");
      expect(url).toContain("to=");
    });
  });

  it("renders events with opaque identifiers only", async () => {
    seedAdmin();
    stubFetch([
      {
        status: 200,
        body: {
          items: [
            {
              id: "e1",
              event: "vault.item.created",
              actor_id: "actor-11111111",
              target_type: "item",
              target_id: "item-22222222",
              result: "success",
              request_id: "req-33333333",
              created_at: "2026-09-06T12:00:00Z",
            },
          ],
          next_cursor: null,
        },
      },
    ]);
    render(<AuditPage />);
    await waitFor(() => expect(screen.getByText(/vault.item.created/)).toBeInTheDocument());
    expect(screen.getByText(/操作者 actor-11…/)).toBeInTheDocument();
  });
});

describe("SettingsPage", () => {
  it("shows system state without any credential values", async () => {
    seedAdmin();
    stubFetch([
      {
        status: 200,
        body: {
          version: "test",
          ready: true,
          checks: { database: true },
          settings: { r2_endpoint: "", r2_bucket: "", r2_prefix: "" },
          r2_credentials_via_file: true,
          scheduler: [
            {
              job: "backup.local",
              status: "failed",
              ran: true,
              started_at: "2026-09-06T03:00:00Z",
              finished_at: "2026-09-06T03:00:10Z",
              detail: "job failed",
            },
          ],
        },
      },
    ]);
    render(<SettingsPage />);

    await waitFor(() => expect(screen.getByText("运行状态")).toBeInTheDocument());
    expect(screen.getByText("由 Secret 文件注入")).toBeInTheDocument();
    // The failed scheduler job is visible on the page.
    expect(screen.getByText(/backup.local/)).toBeInTheDocument();
    // The restore section only ever shows the offline command.
    expect(screen.getByText(/tiny-password restore/)).toBeInTheDocument();
  });

  it("saves the non-sensitive R2 settings whitelist", async () => {
    const user = userEvent.setup();
    seedAdmin();
    const { calls } = stubFetch([
      {
        status: 200,
        body: {
          version: "test",
          ready: true,
          checks: {},
          settings: { r2_endpoint: "", r2_bucket: "", r2_prefix: "" },
          r2_credentials_via_file: false,
          scheduler: [],
        },
      },
      { status: 200, body: { updated: true } },
      {
        status: 200,
        body: {
          version: "test",
          ready: true,
          checks: {},
          settings: { r2_endpoint: "https://acc.r2.cloudflarestorage.com", r2_bucket: "b", r2_prefix: "p" },
          r2_credentials_via_file: false,
          scheduler: [],
        },
      },
    ]);
    render(<SettingsPage />);
    await waitFor(() => expect(screen.getByLabelText("桶名")).toBeInTheDocument());

    await user.type(screen.getByLabelText("S3 端点"), "https://acc.r2.cloudflarestorage.com");
    await user.type(screen.getByLabelText("桶名"), "b");
    await user.type(screen.getByLabelText("对象前缀"), "p");
    await user.click(screen.getByRole("button", { name: "保存设置" }));

    await waitFor(() =>
      expect(calls.some((c) => c.method === "PUT" && c.url.includes("/admin/settings"))).toBe(true),
    );
    const put = calls.find((c) => c.method === "PUT");
    expect(put?.body).toEqual({
      r2_endpoint: "https://acc.r2.cloudflarestorage.com",
      r2_bucket: "b",
      r2_prefix: "p",
    });
  });

  it("reports validation errors from invalid settings", async () => {
    const user = userEvent.setup();
    seedAdmin();
    stubFetch([
      {
        status: 200,
        body: {
          version: "test",
          ready: true,
          checks: {},
          settings: { r2_endpoint: "", r2_bucket: "", r2_prefix: "" },
          r2_credentials_via_file: false,
          scheduler: [],
        },
      },
      { status: 400, body: { code: "VALIDATION_ERROR", message: "invalid" } },
    ]);
    render(<SettingsPage />);
    await waitFor(() => expect(screen.getByLabelText("桶名")).toBeInTheDocument());

    await user.click(screen.getByRole("button", { name: "保存设置" }));
    await waitFor(() => expect(screen.getByText(/配置不符合要求/)).toBeInTheDocument());
  });
});
