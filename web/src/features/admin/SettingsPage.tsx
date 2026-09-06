import { useCallback, useEffect, useState } from "react";
import { ApiError, request } from "../../app/api";
import { useSession } from "../../app/session";
import { Button } from "../../design-system/Button";
import { Field } from "../../design-system/Field";
import { ErrorSummary, StatusBanner } from "../../design-system/Status";

type Settings = {
  r2_endpoint: string;
  r2_bucket: string;
  r2_prefix: string;
};

type SchedulerResult = {
  job: string;
  status: string;
  ran: boolean;
  started_at: string;
  finished_at: string;
  detail: string;
};

type SystemInfo = {
  version: string;
  ready: boolean;
  checks: Record<string, boolean> | null;
  settings: Settings;
  r2_credentials_via_file: boolean;
  scheduler: SchedulerResult[];
};

const errorMessages: Record<string, string> = {
  VALIDATION_ERROR: "配置不符合要求（端点必须是 https URL；桶/前缀格式受限）。",
  FORBIDDEN: "需要管理员角色。",
};

/**
 * SettingsPage: system state (version, readiness, scheduler failures), the
 * non-sensitive R2 delivery settings, and the restore explanation. Secrets
 * always come from mounted Secret files — this page never displays or
 * accepts them. Restore is offline-only: it never touches the running
 * database through the web.
 */
export function SettingsPage() {
  const { csrfToken } = useSession();
  const [info, setInfo] = useState<SystemInfo | null>(null);
  const [form, setForm] = useState<Settings>({ r2_endpoint: "", r2_bucket: "", r2_prefix: "" });
  const [error, setError] = useState<string | null>(null);
  const [requestId, setRequestId] = useState<string | undefined>();
  const [banner, setBanner] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const fail = useCallback((err: unknown) => {
    if (err instanceof ApiError) {
      setError(errorMessages[err.code] ?? err.message);
      setRequestId(err.requestId);
    } else {
      setError("网络错误，请重试。");
    }
  }, []);

  const load = useCallback(async () => {
    try {
      const data = await request<SystemInfo>("GET", "/api/v1/admin/settings", undefined, { csrfToken });
      setInfo(data);
      setForm(data.settings);
      setError(null);
    } catch (err) {
      fail(err);
    }
  }, [csrfToken, fail]);

  useEffect(() => {
    void load();
  }, [load]);

  const save = async (event: React.FormEvent) => {
    event.preventDefault();
    setBusy(true);
    try {
      await request("PUT", "/api/v1/admin/settings", form, { csrfToken });
      setBanner("系统设置已保存。");
      await load();
    } catch (err) {
      fail(err);
    } finally {
      setBusy(false);
    }
  };

  const failingJobs = info?.scheduler.filter((r) => r.status === "failed") ?? [];

  return (
    <div className="mx-auto max-w-3xl px-4 py-10">
      <header>
        <h2 className="font-display text-3xl font-bold">系统设置</h2>
        <p className="mt-1 font-body text-sm text-neutral-600">
          仅包含非敏感配置；所有凭据（主密钥、备份口令、R2 密钥）通过挂载的 Secret 文件注入，本页不显示、不回传。
        </p>
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

      {info === null ? (
        <p className="font-body text-sm text-neutral-600">正在加载系统状态…</p>
      ) : (
        <>
          <section className="mt-2 border border-ink p-6" aria-labelledby="system-state-heading">
            <h3 id="system-state-heading" className="font-display text-xl font-bold">
              运行状态
            </h3>
            <dl className="mt-3 space-y-1 font-body text-sm">
              <div className="flex justify-between gap-4">
                <dt>版本</dt>
                <dd className="font-mono">{info.version}</dd>
              </div>
              <div className="flex justify-between gap-4">
                <dt>就绪状态</dt>
                <dd className="font-mono">{info.ready ? "就绪" : "未就绪"}</dd>
              </div>
              {info.checks &&
                Object.entries(info.checks).map(([name, ok]) => (
                  <div key={name} className="flex justify-between gap-4">
                    <dt>检查 · {name}</dt>
                    <dd className="font-mono">{ok ? "通过" : "失败"}</dd>
                  </div>
                ))}
              <div className="flex justify-between gap-4">
                <dt>R2 凭据</dt>
                <dd className="font-mono">{info.r2_credentials_via_file ? "由 Secret 文件注入" : "未挂载"}</dd>
              </div>
            </dl>
            {failingJobs.length > 0 && (
              <div role="alert" className="mt-4 border-l-4 border-accent bg-neutral-100 px-4 py-3">
                <p className="font-body text-sm font-semibold">最近有后台任务失败：</p>
                <ul className="mt-1 list-disc pl-5 font-mono text-xs">
                  {failingJobs.map((job) => (
                    <li key={job.job}>
                      {job.job} · {job.detail || "失败"}
                    </li>
                  ))}
                </ul>
              </div>
            )}
          </section>

          <form className="mt-6 space-y-4 border border-ink p-6" onSubmit={save} noValidate>
            <h3 className="font-display text-xl font-bold">R2 备份目标（非敏感部分）</h3>
            <Field
              id="r2-endpoint"
              label="S3 端点"
              autoComplete="off"
              value={form.r2_endpoint}
              onChange={(e) => setForm({ ...form, r2_endpoint: e.target.value })}
              hint="形如 https://<账号>.r2.cloudflarestorage.com。"
            />
            <Field
              id="r2-bucket"
              label="桶名"
              autoComplete="off"
              value={form.r2_bucket}
              onChange={(e) => setForm({ ...form, r2_bucket: e.target.value })}
            />
            <Field
              id="r2-prefix"
              label="对象前缀"
              autoComplete="off"
              value={form.r2_prefix}
              onChange={(e) => setForm({ ...form, r2_prefix: e.target.value })}
            />
            <Button type="submit" disabled={busy}>
              {busy ? "保存中…" : "保存设置"}
            </Button>
          </form>

          <section className="mt-6 border border-ink p-6" aria-labelledby="restore-info-heading">
            <h3 id="restore-info-heading" className="font-display text-xl font-bold">
              整实例恢复
            </h3>
            <p className="mt-2 font-body text-sm text-neutral-700">
              整实例恢复不会在网页中执行。请在停止服务后，在容器内运行离线命令：
            </p>
            <p className="mt-2 select-all border border-divider bg-neutral-100 px-3 py-2 font-mono text-sm">
              tiny-password restore /restore/backup.7z
            </p>
            <p className="mt-2 font-body text-sm text-neutral-700">
              恢复会使用归档内密钥解密并以本实例挂载的新主密钥重加密；恢复前自动生成前置快照，失败时自动回滚原库。
            </p>
          </section>
        </>
      )}
    </div>
  );
}
