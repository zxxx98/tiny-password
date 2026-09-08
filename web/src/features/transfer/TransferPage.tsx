import { useCallback, useEffect, useRef, useState } from "react";
import { ApiError, request, requestBlob } from "../../app/api";
import { SESSION_EXPIRED_EVENT, useSession } from "../../app/session";
import { Button } from "../../design-system/Button";
import { Field } from "../../design-system/Field";
import { ErrorSummary, StatusBanner } from "../../design-system/Status";

type Preview = {
  preview_token: string;
  counts: Record<string, number>;
  conflicts: number;
  missing_references: string[];
};

type DownloadProgress = {
  receivedBytes: number;
  totalBytes?: number;
};

const errorOf = (err: unknown): { message: string; requestId?: string } => {
  if (err instanceof ApiError) {
    return { message: err.message, requestId: err.requestId };
  }
  return { message: "网络错误，请重试。" };
};

const revokeDownloadUrl = (url: string): void => {
  if (typeof URL.revokeObjectURL === "function") {
    URL.revokeObjectURL(url);
  }
};

/**
 * TransferPage (design §11.4, decision D10): personal export as an encrypted
 * 7z download, and a two-phase import — preview first (zero database
 * writes), then an explicit confirm.
 */
export function TransferPage() {
  const { csrfToken } = useSession();
  const mountedRef = useRef(true);
  const sessionExpiredRef = useRef(false);
  const csrfTokenRef = useRef(csrfToken);
  csrfTokenRef.current = csrfToken;
  const previewRef = useRef<Preview | null>(null);
  const controllersRef = useRef(new Set<AbortController>());
  const downloadUrlRef = useRef<string | null>(null);
  const downloadCleanupRef = useRef<ReturnType<typeof window.setTimeout> | null>(null);
  const sensitiveRef = useRef<{ passphrase: string; confirm: string; importPass: string; file: File | null }>({
    passphrase: "",
    confirm: "",
    importPass: "",
    file: null,
  });

  const [passphrase, setPassphrase] = useState("");
  const [confirm, setConfirm] = useState("");
  const [exporting, setExporting] = useState(false);
  const [exportProgress, setExportProgress] = useState<DownloadProgress | null>(null);

  const [file, setFile] = useState<File | null>(null);
  const [importPass, setImportPass] = useState("");
  const [preview, setPreview] = useState<Preview | null>(null);
  const [importing, setImporting] = useState(false);

  const [error, setError] = useState<string | null>(null);
  const [requestId, setRequestId] = useState<string | undefined>();
  const [banner, setBanner] = useState<string | null>(null);
  const [fileInputKey, setFileInputKey] = useState(0);

  sensitiveRef.current.passphrase = passphrase;
  sensitiveRef.current.confirm = confirm;
  sensitiveRef.current.importPass = importPass;
  sensitiveRef.current.file = file;

  const clearSecrets = useCallback(() => {
    sensitiveRef.current = { passphrase: "", confirm: "", importPass: "", file: null };
    previewRef.current = null;
    setPassphrase("");
    setConfirm("");
    setFile(null);
    setImportPass("");
    setPreview(null);
    setFileInputKey((key) => key + 1);
    setError(null);
    setRequestId(undefined);
    setBanner(null);
    setExportProgress(null);
  }, []);

  const cancelPreview = useCallback(async (previewToken: string, token: string) => {
    try {
      await request<void>(
        "POST",
        "/api/v1/transfer/import/cancel",
        { preview_token: previewToken },
        // Session expiry is already being handled by the originating call;
        // cleanup must not recursively broadcast another expiry event.
        { csrfToken: token, suppressAuthFailure: true },
      );
    } catch {
      // Best effort: the server's lifecycle cleanup is authoritative.
    }
  }, []);

  useEffect(() => {
    mountedRef.current = true;
    const onExpired = () => {
      sessionExpiredRef.current = true;
      for (const controller of controllersRef.current) {
        controller.abort();
      }
      const token = previewRef.current?.preview_token;
      clearSecrets();
      if (token) {
        void cancelPreview(token, csrfTokenRef.current);
      }
    };
    window.addEventListener(SESSION_EXPIRED_EVENT, onExpired);
    return () => {
      mountedRef.current = false;
      for (const controller of controllersRef.current) {
        controller.abort();
      }
      if (downloadCleanupRef.current !== null) {
        window.clearTimeout(downloadCleanupRef.current);
      }
      if (downloadUrlRef.current) {
        revokeDownloadUrl(downloadUrlRef.current);
      }
      downloadCleanupRef.current = null;
      downloadUrlRef.current = null;
      const token = previewRef.current?.preview_token;
      // React state is no longer observable after unmount; clear the refs
      // directly so no cleanup callback retains file or passphrase values.
      sensitiveRef.current = { passphrase: "", confirm: "", importPass: "", file: null };
      previewRef.current = null;
      if (token) {
        void cancelPreview(token, csrfTokenRef.current);
      }
      window.removeEventListener(SESSION_EXPIRED_EVENT, onExpired);
    };
  }, [cancelPreview, clearSecrets]);

  const beginRequest = () => {
    const controller = new AbortController();
    controllersRef.current.add(controller);
    return controller;
  };

  const endRequest = (controller: AbortController) => {
    controllersRef.current.delete(controller);
  };

  const fail = useCallback((err: unknown) => {
    if (!mountedRef.current || sessionExpiredRef.current) {
      return;
    }
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
    const passphraseBytes = new TextEncoder().encode(passphrase).byteLength;
    if (passphraseBytes < 12 || passphraseBytes > 1024) {
      setError("归档口令长度需为 12–1024 字节。");
      return;
    }
    if (/\r|\n/.test(passphrase)) {
      setError("归档口令不能包含换行。");
      return;
    }
    setExporting(true);
    setExportProgress(null);
    const controller = beginRequest();
    try {
      const blob = await requestBlob(
        "POST",
        "/api/v1/transfer/export",
        { passphrase, passphrase_confirm: confirm },
        {
          csrfToken,
          signal: controller.signal,
          onDownloadProgress: (receivedBytes, totalBytes) => {
            if (!mountedRef.current || sessionExpiredRef.current || controller.signal.aborted) return;
            setExportProgress({ receivedBytes, totalBytes });
          },
        },
      );
      if (!mountedRef.current || sessionExpiredRef.current || controller.signal.aborted) {
        return;
      }
      const url = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = url;
      link.download = "tiny-password-export.7z";
      link.style.display = "none";
      document.body.appendChild(link);
      link.click();
      link.remove();
      // Let the browser claim the object URL before releasing it. Revoking
      // synchronously can make the download appear to start while saving an
      // empty/missing file in some browsers.
      downloadUrlRef.current = url;
      downloadCleanupRef.current = window.setTimeout(() => {
        revokeDownloadUrl(url);
        if (downloadUrlRef.current === url) {
          downloadUrlRef.current = null;
          downloadCleanupRef.current = null;
        }
      }, 1000);
      setPassphrase("");
      setConfirm("");
      setBanner("归档已下载。请妥善保管归档口令——没有口令将无法恢复。");
    } catch (err) {
      if (!mountedRef.current || sessionExpiredRef.current || controller.signal.aborted) {
        return;
      }
      const info = errorOf(err);
      setError(info.message);
      setRequestId(info.requestId);
    } finally {
      endRequest(controller);
      if (mountedRef.current && !sessionExpiredRef.current && !controller.signal.aborted) {
        setExporting(false);
        setExportProgress(null);
      }
    }
  }, [passphrase, confirm, csrfToken]);

  const doPreview = useCallback(async () => {
    setError(null);
    const previousToken = previewRef.current?.preview_token;
    if (previousToken) {
      previewRef.current = null;
      void cancelPreview(previousToken, csrfTokenRef.current);
    }
    setPreview(null);
    previewRef.current = null;
    if (!file) {
      setError("请选择要导入的归档文件。");
      return;
    }
    setImporting(true);
    const controller = beginRequest();
    try {
      const body = new FormData();
      body.append("archive", file);
      body.append("passphrase", importPass);
      const data = await request<Preview>(
        "POST",
        "/api/v1/transfer/import/preview",
        body,
        { csrfToken, signal: controller.signal },
      );
      if (!mountedRef.current || sessionExpiredRef.current || controller.signal.aborted) {
        return;
      }
      previewRef.current = data;
      setPreview(data);
    } catch (err) {
      if (!mountedRef.current || sessionExpiredRef.current || controller.signal.aborted) {
        return;
      }
      const info = errorOf(err);
      setError(info.message);
      setRequestId(info.requestId);
    } finally {
      endRequest(controller);
      if (mountedRef.current && !sessionExpiredRef.current && !controller.signal.aborted) {
        setImporting(false);
      }
    }
  }, [file, importPass, csrfToken, cancelPreview]);

  const doConfirm = useCallback(async () => {
    if (!preview) {
      return;
    }
    setImporting(true);
    setError(null);
    const controller = beginRequest();
    try {
      const summary = await request<{ imported_count: number }>(
        "POST",
        "/api/v1/transfer/import/confirm",
        { preview_token: preview.preview_token },
        { csrfToken, signal: controller.signal },
      );
      if (!mountedRef.current || sessionExpiredRef.current || controller.signal.aborted) {
        return;
      }
      previewRef.current = null;
      setPreview(null);
      setFile(null);
      setFileInputKey((key) => key + 1);
      setImportPass("");
      setBanner(`导入完成：${summary.imported_count} 个条目已写入保险库。`);
    } catch (err) {
      if (!controller.signal.aborted) {
        fail(err);
      }
    } finally {
      endRequest(controller);
      if (mountedRef.current && !sessionExpiredRef.current && !controller.signal.aborted) {
        setImporting(false);
      }
    }
  }, [preview, csrfToken, fail]);

  const exportLabel =
    exportProgress?.totalBytes && exportProgress.totalBytes > 0
      ? `正在下载… ${Math.min(100, Math.round((exportProgress.receivedBytes / exportProgress.totalBytes) * 100))}%`
      : "正在生成归档…";

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
          归档使用你输入的口令加密（12–1024 字节）。口令不会存储在任何地方——丢失后归档无法恢复。
        </p>
        <Field
          id="export-passphrase"
          label="归档口令"
          type="password"
          autoComplete="new-password"
          minLength={12}
          maxLength={1024}
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
          {exporting ? exportLabel : "下载加密归档"}
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
          key={fileInputKey}
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
