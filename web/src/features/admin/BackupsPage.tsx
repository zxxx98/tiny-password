import { useCallback, useEffect, useState } from "react";
import { ApiError, request } from "../../app/api";
import { useSession } from "../../app/session";
import { Button } from "../../design-system/Button";
import { Field } from "../../design-system/Field";
import { ErrorSummary, StatusBanner } from "../../design-system/Status";

type JobConfig = {
  target: "local" | "r2";
  enabled: boolean;
  schedule_time: string;
  schedule_timezone: string;
  retention_daily: number;
  retention_weekly: number;
  retention_monthly: number;
  delivery_ready: boolean;
};

type RunRow = {
  id: string;
  target: string;
  status: "pending" | "running" | "succeeded" | "failed" | "interrupted";
  triggered_by: "manual" | "scheduled";
  started_at: string;
  finished_at: string | null;
  size_bytes: number | null;
  error_code: string | null;
};

type JobsPage = { jobs: JobConfig[] };
type RunsPage = { items: RunRow[]; next_cursor: string | null };

const targetLabels: Record<JobConfig["target"], string> = {
  local: "本地备份",
  r2: "R2 备份",
};

const statusLabels: Record<RunRow["status"], string> = {
  pending: "等待中",
  running: "运行中",
  succeeded: "成功",
  failed: "失败",
  interrupted: "已中断",
};

const errorMessages: Record<string, string> = {
  VALIDATION_ERROR: "配置不符合要求（时间 HH:MM、IANA 时区、保留数量 0–999）。",
  BACKUP_BUSY: "已有备份在运行，请稍后再试。",
  MAINTENANCE: "备份口令或目标交付未配置：请挂载 backup_passphrase Secret，并完成 R2 的端点/桶配置与凭据挂载。",
  FORBIDDEN: "需要管理员角色。",
};

const selectClasses =
  "w-full min-h-[44px] border-b-2 border-ink bg-transparent px-3 py-2 font-mono text-sm " +
  "focus-visible:bg-neutral-100 focus-visible:outline-none";

const fallbackTimezones = [
  "America/Los_Angeles",
  "America/New_York",
  "Europe/London",
  "Asia/Shanghai",
  "Asia/Hong_Kong",
  "Asia/Tokyo",
  "Asia/Singapore",
  "Australia/Sydney",
];

function getTimezoneOptions(current: string) {
  const intl = Intl as typeof Intl & {
    supportedValuesOf?: (key: "timeZone") => string[];
  };
  let supported: string[] = [];
  try {
    supported = intl.supportedValuesOf?.("timeZone") ?? [];
  } catch {
    supported = [];
  }

  const options = new Set(["UTC", ...(supported.length > 0 ? supported : fallbackTimezones)]);
  if (current) options.add(current);
  return [...options].sort((a, b) => {
    if (a === "UTC") return -1;
    if (b === "UTC") return 1;
    return a.localeCompare(b);
  });
}

/**
 * BackupsPage: per-target schedule/enabled/retention configuration, a
 * manual run trigger, and the independent run history. Local and R2 results
 * are always reported separately (design §11.2).
 */
export function BackupsPage() {
  const { csrfToken } = useSession();
  const [jobs, setJobs] = useState<JobConfig[] | null>(null);
  const [runs, setRuns] = useState<RunRow[]>([]);
  const [cursor, setCursor] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [requestId, setRequestId] = useState<string | undefined>();
  const [banner, setBanner] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  const fail = useCallback((err: unknown) => {
    if (err instanceof ApiError) {
      setError(errorMessages[err.code] ?? err.message);
      setRequestId(err.requestId);
    } else {
      setError("网络错误，请重试。");
    }
  }, []);

  const load = useCallback(
    async (nextCursor: string | null, append: boolean) => {
      try {
        const path = nextCursor ? `/api/v1/admin/backups/runs?cursor=${encodeURIComponent(nextCursor)}` : "/api/v1/admin/backups/runs";
        const jobsPage = await request<JobsPage>("GET", "/api/v1/admin/backups/jobs", undefined, { csrfToken });
        const runsPage = await request<RunsPage>("GET", path, undefined, { csrfToken });
        setJobs(jobsPage.jobs ?? []);
        setRuns((prev) => (append && nextCursor ? [...prev, ...(runsPage.items ?? [])] : (runsPage.items ?? [])));
        setCursor(runsPage.next_cursor ?? null);
        setError(null);
      } catch (err) {
        fail(err);
      }
    },
    [csrfToken, fail],
  );

  useEffect(() => {
    void load(null, false);
  }, [load]);

  const saveJob = async (job: JobConfig) => {
    setBusy("save-" + job.target);
    try {
      // Only the whitelisted fields are sent; the server rejects unknown ones.
      await request(
        "PUT",
        "/api/v1/admin/backups/jobs",
        {
          target: job.target,
          enabled: job.enabled,
          schedule_time: job.schedule_time,
          schedule_timezone: job.schedule_timezone,
          retention_daily: job.retention_daily,
          retention_weekly: job.retention_weekly,
          retention_monthly: job.retention_monthly,
        },
        { csrfToken },
      );
      setBanner(`${targetLabels[job.target]}配置已保存。`);
      await load(null, false);
    } catch (err) {
      fail(err);
    } finally {
      setBusy(null);
    }
  };

  const runNow = async (targets: string[]) => {
    setBusy("run-" + targets.join("-"));
    try {
      await request("POST", "/api/v1/admin/backups/run", { targets }, { csrfToken });
      setBanner("备份已开始，完成后可在下方历史中查看结果。");
      // Give the run a moment, then refresh the history once.
      setTimeout(() => void load(null, false), 3000);
    } catch (err) {
      fail(err);
    } finally {
      setBusy(null);
    }
  };

  const patchJob = (target: string, patch: Partial<JobConfig>) => {
    setJobs((prev) => prev?.map((j) => (j.target === target ? { ...j, ...patch } : j)) ?? null);
  };

  // A target without its delivery configuration can never run; the
  // all-targets shortcut only lights up when every target is ready.
  const allReady = (jobs ?? []).length > 0 && (jobs ?? []).every((j) => j.delivery_ready);

  return (
    <div className="mx-auto max-w-3xl px-4 py-10">
      <header className="flex flex-wrap items-center justify-between gap-4">
        <div>
          <h2 className="font-display text-3xl font-bold">实例备份</h2>
          <p className="mt-1 font-body text-sm text-neutral-600">
            每个目标独立调度、独立报告；仅保存非敏感配置，口令与凭据来自挂载的 Secret。
          </p>
        </div>
        <Button disabled={!allReady || busy === "run-local-r2"} onClick={() => void runNow(["local", "r2"])}>
          {busy === "run-local-r2" ? "启动中…" : "立即备份全部目标"}
        </Button>
      </header>

      <div className="mt-6">
        <ErrorSummary message={error ?? ""} requestId={requestId} onDismiss={() => setError(null)} />
        {banner && (
          <StatusBanner>
            {banner}{" "}
            <button
              type="button"
              aria-label="关闭状态提示"
              className="font-mono text-xs uppercase tracking-widest underline-offset-4 hover:underline"
              onClick={() => setBanner(null)}
            >
              知道了
            </button>
          </StatusBanner>
        )}
      </div>

      {jobs === null ? (
        <p className="font-body text-sm text-neutral-600">正在加载备份配置…</p>
      ) : (
        <ul className="mt-2 space-y-6">
          {jobs.map((job) => (
            <li key={job.target} className="border border-ink p-6">
              <div className="flex flex-wrap items-center justify-between gap-3">
                <h3 className="font-display text-xl font-bold">
                  {targetLabels[job.target]}
                  {!job.delivery_ready && (
                    <span className="ml-2 border border-ink px-1 py-0.5 font-mono text-xs uppercase">
                      未配置交付
                    </span>
                  )}
                </h3>
                <div className="flex gap-2">
                  <Button
                    variant="secondary"
                    disabled={!job.delivery_ready || busy === "run-" + job.target}
                    title={job.delivery_ready ? undefined : "该目标的交付配置未完成"}
                    onClick={() => void runNow([job.target])}
                  >
                    {busy === "run-" + job.target ? "启动中…" : "立即执行"}
                  </Button>
                  <Button
                    disabled={busy === "save-" + job.target}
                    onClick={() => void saveJob(job)}
                  >
                    {busy === "save-" + job.target ? "保存中…" : "保存配置"}
                  </Button>
                </div>
              </div>

              <div className="mt-4 grid gap-4 sm:grid-cols-2">
                <label className="flex items-center gap-2 font-body text-sm">
                  <input
                    type="checkbox"
                    aria-label={`启用${targetLabels[job.target]}`}
                    checked={job.enabled}
                    onChange={(e) => patchJob(job.target, { enabled: e.target.checked })}
                  />
                  启用每日调度
                </label>
                <Field
                  id={`schedule-${job.target}`}
                  label="每日执行时间"
                  type="time"
                  step={60}
                  value={job.schedule_time}
                  onChange={(e) => patchJob(job.target, { schedule_time: e.target.value })}
                  hint="使用时间选择器设置本目标时区下的每日执行时间；留空表示不按日调度。"
                />
                <div className="space-y-1">
                  <label
                    htmlFor={`timezone-${job.target}`}
                    className="block font-mono text-xs uppercase tracking-widest"
                  >
                    时区（IANA）
                  </label>
                  <select
                    id={`timezone-${job.target}`}
                    name={`timezone-${job.target}`}
                    className={selectClasses}
                    value={job.schedule_timezone || "UTC"}
                    onChange={(e) => patchJob(job.target, { schedule_timezone: e.target.value })}
                  >
                    {getTimezoneOptions(job.schedule_timezone).map((timezone) => (
                      <option key={timezone} value={timezone}>
                        {timezone}
                      </option>
                    ))}
                  </select>
                  <p className="font-body text-xs text-neutral-500">
                    从浏览器支持的 IANA 时区中选择；旧配置留空时按 UTC 显示并继续按 UTC 执行。
                  </p>
                </div>
                <Field
                  id={`retention-daily-${job.target}`}
                  label="日备份保留数"
                  type="number"
                  min={0}
                  max={999}
                  step={1}
                  inputMode="numeric"
                  value={String(job.retention_daily)}
                  onChange={(e) => patchJob(job.target, { retention_daily: Number(e.target.value) || 0 })}
                />
                <Field
                  id={`retention-weekly-${job.target}`}
                  label="周备份保留数"
                  type="number"
                  min={0}
                  max={999}
                  step={1}
                  inputMode="numeric"
                  value={String(job.retention_weekly)}
                  onChange={(e) => patchJob(job.target, { retention_weekly: Number(e.target.value) || 0 })}
                />
                <Field
                  id={`retention-monthly-${job.target}`}
                  label="月备份保留数"
                  type="number"
                  min={0}
                  max={999}
                  step={1}
                  inputMode="numeric"
                  value={String(job.retention_monthly)}
                  onChange={(e) => patchJob(job.target, { retention_monthly: Number(e.target.value) || 0 })}
                />
              </div>
            </li>
          ))}
        </ul>
      )}

      <section className="mt-10" aria-labelledby="runs-heading">
        <h3 id="runs-heading" className="font-display text-xl font-bold">
          执行历史
        </h3>
        {runs.length === 0 ? (
          <p className="mt-2 font-body text-sm text-neutral-600">从未执行过备份。失败的任务也会在这里逐目标显示。</p>
        ) : (
          <ul className="mt-3 divide-y divide-divider border-y border-divider">
            {runs.map((run) => (
              <li key={run.id} className="flex flex-wrap items-center justify-between gap-3 py-3">
                <div>
                  <p className="font-body text-sm font-semibold">
                    {targetLabels[run.target as JobConfig["target"]] ?? run.target} ·{" "}
                    <span className="font-mono text-xs">{statusLabels[run.status]}</span>
                    {run.error_code && <span className="ml-2 font-mono text-xs">{run.error_code}</span>}
                  </p>
                  <p className="mt-1 font-mono text-xs text-neutral-500">
                    {run.started_at.slice(0, 19).replace("T", " ")} · {run.triggered_by === "manual" ? "手动" : "定时"}
                    {run.size_bytes != null && ` · ${(run.size_bytes / 1024).toFixed(0)} KiB`}
                  </p>
                </div>
              </li>
            ))}
          </ul>
        )}
        {cursor && (
          <Button variant="secondary" className="mt-4" onClick={() => void load(cursor, true)}>
            加载更多
          </Button>
        )}
      </section>
    </div>
  );
}
