import type { ReactNode } from "react";

/**
 * ErrorSummary renders one dismissible, assertive error region per page:
 * the stable place API errors (request_id included) are surfaced.
 */
export function ErrorSummary({ message, requestId, onDismiss }: { message: string; requestId?: string; onDismiss?: () => void }) {
  if (!message) {
    return null;
  }
  return (
    <div role="alert" className="mb-4 border-l-4 border-accent bg-neutral-100 px-4 py-3">
      <p className="font-body text-sm">{message}</p>
      {requestId && (
        <p className="mt-1 font-mono text-xs text-neutral-500">request_id: {requestId}</p>
      )}
      {onDismiss && (
        <button
          type="button"
          onClick={onDismiss}
          aria-label="关闭错误提示"
          className="mt-2 block min-h-[44px] font-mono text-xs uppercase tracking-widest text-neutral-600 underline-offset-4 hover:underline"
        >
          知道了
        </button>
      )}
    </div>
  );
}

/** StatusBanner announces completed background work (polite region). */
export function StatusBanner({ children }: { children: ReactNode }) {
  return (
    <div role="status" className="mb-4 border border-ink bg-paper px-4 py-3 font-body text-sm">
      {children}
    </div>
  );
}

/** Loading placeholder with an accessible busy label. */
export function Loading({ label = "加载中…" }: { label?: string }) {
  return (
    <p role="status" className="font-body text-sm text-neutral-600">
      {label}
    </p>
  );
}
