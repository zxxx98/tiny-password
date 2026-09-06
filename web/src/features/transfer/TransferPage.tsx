import { useCallback, useState } from "react";
import { ApiError, request } from "../../app/api";
import { useSession } from "../../app/session";
import { Button } from "../../design-system/Button";
import { Field } from "../../design-system/Field";
import { ErrorSummary, StatusBanner } from "../../design-system/Status";

type Preview = {
  preview_token: string;
  counts: Record<string, number>;
  conflicts: number;
  missing_references: string[];
};

const errorOf = (err: unknown): { message: string; requestId?: string } => {
  if (err instanceof ApiError) {
    return { message: err.message, requestId: err.requestId };
  }
  return { message: "网络错误，请重试。" };
};

/**
 * TransferPage (design §11.4, decision D10): personal export as an encrypted
 * 7z download, and a two-phase import — preview first (zero database
 * writes), then an explicit confirm.
 */
export function TransferPage() {
  const { csrfToken } = useSession();

  const [passphrase, setPassphrase] = useState("");
  const [confirm, setConfirm] = useState("");
  const [exporting, setExporting] = useState(false);

  const [file, setFile] = useState<File | null>(null);
  const [importPass, setImportPass] = useState("");
  const [preview, setPreview] = useState<Preview | null>(null);
  const [importing, setImporting] = useState(false);

  const [error, setError] = useState<string | null>(null);
  const [requestId, setRequestId] = useState<string | undefined>();
  const [banner, setBanner] = useState<string | null>(null);

  const fail = useCallback((err: unknown) => {
    if (err instanceof ApiError) {
      setError(err.message);
      setRequestId(err.requestId);
    } else {
      setError("网络错误，请重试。");
    }
  }, []);

  const doExport = useCallback(async () => {
    setError(null);
    if (passphrase !== confirm) {
      setError("两次输入的归档口令不一致。");
      return;
    }
    setExporting(true);
    try {
      const response = await fetch("/api/v1/transfer/export", {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken },
        body: JSON.stringify({ passphrase, passphrase_confirm: confirm }),
      });
      if (!response.ok) {
        const body = await response.json().catch(() => ({ message: "导出失败" }));
        setError(body.message ?? "导出失败");
        return;
      }
      const blob = await response.blob();
      const url = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = url;
      link.download = "tiny-password-export.7z";
      link.click();
      URL.revokeObjectURL(url);
      setPassphrase("");
      setConfirm("");
      setBanner("归档已下载。请妥善保管归档口令——没有口令将无法恢复。");
    } catch (err) {
      const info = errorOf(err);
      setError(info.message);
      setRequestId(info.requestId);
    } finally {
      setExporting(false);
    }
  }, [passphrase, confirm, csrfToken]);

  const doPreview = useCallback(async () => {
    setError(null);
    setPreview(null);
    if (!file) {
      setError("请选择要导入的归档文件。");
      return;
    }
    setImporting(true);
    try {
      const body = new FormData();
      body.append("archive", file);
      body.append("passphrase", importPass);
      const response = await fetch("/api/v1/transfer/import/preview", {
        method: "POST",
        credentials: "same-origin",
        headers: { "X-CSRF-Token": csrfToken },
        body,
      });
      const data = await response.json().catch(() => ({ message: "导入失败" }));
      if (!response.ok) {
        setError(data.message ?? "导入失败");
        return;
      }
      setPreview(data as Preview);
    } catch {
      setError("导入失败，请重试。");
    } finally {
      setImporting(false);
    }
  }, [file, importPass, csrfToken]);

  const doConfirm = useCallback(async () => {
    if (!preview) {
      return;
    }
    setImporting(true);
    setError(null);
    try {
      const summary = await request<{ imported_count: number }>(
        "POST",
        "/api/v1/transfer/import/confirm",
        { preview_token: preview.preview_token },
        { csrfToken },
      );
      setPreview(null);
      setFile(null);
      setImportPass("");
      setBanner(`导入完成：${summary.imported_count} 个条目已写入保险库。`);
    } catch (err) {
      fail(err);
    } finally {
      setImporting(false);
    }
  }, [preview, csrfToken, fail]);

  return (
    <div className="mx-auto max-w-3xl space-y-8 px-4 py-10">
      <header>
        <h2 className="font-display text-3xl font-bold">导入 / 导出</h2>
        <p className="mt-1 font-body text-sm text-neutral-600">
          导出内容为你的个人条目与你创建的共享条目的当前版本（不含历史与回收站，D10）。
        </p>
      </header>

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

      <section aria-labelledby="export-heading" className="space-y-4 border border-ink p-6">
        <h3 id="export-heading" className="font-display text-xl font-bold">
          导出
        </h3>
        <p className="font-body text-xs text-neutral-500">
          归档使用你输入的口令加密（12–1024 字符）。口令不会存储在任何地方——丢失后归档无法恢复。
        </p>
        <Field
          id="export-passphrase"
          label="归档口令"
          type="password"
          autoComplete="new-password"
          minLength={12}
          value={passphrase}
          onChange={(e) => setPassphrase(e.target.value)}
        />
        <Field
          id="export-confirm"
          label="确认归档口令"
          type="password"
          autoComplete="new-password"
          value={confirm}
          onChange={(e) => setConfirm(e.target.value)}
        />
        <Button disabled={exporting} onClick={() => void doExport()}>
          {exporting ? "正在导出…" : "下载加密归档"}
        </Button>
      </section>

      <section aria-labelledby="import-heading" className="space-y-4 border border-ink p-6">
        <h3 id="import-heading" className="font-display text-xl font-bold">
          导入
        </h3>
        <p className="font-body text-xs text-neutral-500">
          预览不写入任何数据；确认后一次事务完成导入。冲突的条目 ID 会重新编号，不会被覆盖。
        </p>
        <Field
          id="import-file"
          label="归档文件（.7z）"
          type="file"
          accept=".7z"
          onChange={(e) => setFile(e.target.files?.[0] ?? null)}
        />
        <Field
          id="import-passphrase"
          label="归档口令（导入）"
          type="password"
          autoComplete="off"
          value={importPass}
          onChange={(e) => setImportPass(e.target.value)}
        />
        <Button disabled={importing} onClick={() => void doPreview()}>
          {importing ? "处理中…" : "预览导入"}
        </Button>

        {preview && (
          <div className="space-y-3 border border-ink p-4" role="region" aria-label="导入预览">
            <h4 className="font-display text-lg font-bold">预览</h4>
            <ul className="font-body text-sm">
              {Object.entries(preview.counts).map(([type, count]) => (
                <li key={type}>
                  {type}: {count}
                </li>
              ))}
            </ul>
            <p className="font-body text-sm">
              冲突条目（将重新编号）：<span className="font-mono">{preview.conflicts}</span>
            </p>
            {preview.missing_references.length > 0 && (
              <p className="font-body text-sm text-accent">
                有 {preview.missing_references.length} 个地址引用指向归档之外，导入后需要手动补全。
              </p>
            )}
            <Button onClick={() => void doConfirm()} disabled={importing}>
              {importing ? "导入中…" : "确认导入"}
            </Button>
          </div>
        )}
      </section>
    </div>
  );
}
