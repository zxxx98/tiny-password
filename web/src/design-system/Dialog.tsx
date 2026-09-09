import { useEffect, useId, useRef, type ReactNode, type RefObject } from "react";
import { Button } from "./Button";

export type DialogProps = {
  open: boolean;
  title: string;
  description?: ReactNode;
  children: ReactNode;
  footer?: ReactNode;
  onClose: () => void;
  initialFocusRef?: RefObject<HTMLElement | null>;
  dismissible?: boolean;
  danger?: boolean;
};

export type ConfirmDialogProps = {
  open: boolean;
  title: string;
  description?: ReactNode;
  confirmLabel?: string;
  cancelLabel?: string;
  confirmDisabled?: boolean;
  /** Danger confirmations (e.g. irreversible deletes) get the accent border. */
  danger?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
};

const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

let bodyLockCount = 0;
let previousBodyOverflow = "";

function lockBody() {
  if (bodyLockCount === 0) {
    previousBodyOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
  }
  bodyLockCount += 1;
}

function unlockBody() {
  bodyLockCount = Math.max(0, bodyLockCount - 1);
  if (bodyLockCount === 0) {
    document.body.style.overflow = previousBodyOverflow;
    previousBodyOverflow = "";
  }
}

function focusFirst(dialog: HTMLDialogElement, initialFocusRef?: RefObject<HTMLElement | null>) {
  const initial = initialFocusRef?.current;
  const focusable = dialog.querySelector<HTMLElement>(FOCUSABLE);
  (initial ?? focusable ?? dialog).focus();
}

/**
 * Shared newsprint modal. The native dialog supplies top-layer and inert
 * behavior in browsers; the small keyboard fallback keeps the same contract
 * in browsers and test environments without dialog support.
 */
export function Dialog({
  open,
  title,
  description,
  children,
  footer,
  onClose,
  initialFocusRef,
  dismissible = true,
  danger = false,
}: DialogProps) {
  const dialogRef = useRef<HTMLDialogElement>(null);
  const restoreRef = useRef<HTMLElement | null>(null);
  const titleId = useId();
  const descriptionId = useId();

  useEffect(() => {
    if (!open) {
      return;
    }

    const dialog = dialogRef.current;
    if (!dialog) {
      return;
    }
    restoreRef.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    lockBody();

    try {
      if (typeof dialog.showModal === "function" && !dialog.open) {
        dialog.showModal();
      } else if (!dialog.open) {
        dialog.setAttribute("open", "");
      }
    } catch {
      // jsdom and older browsers expose dialog without implementing showModal.
      dialog.setAttribute("open", "");
    }
    focusFirst(dialog, initialFocusRef);

    return () => {
      if (dialog.open && typeof dialog.close === "function") {
        try {
          dialog.close();
        } catch {
          dialog.removeAttribute("open");
        }
      } else {
        dialog.removeAttribute("open");
      }
      unlockBody();
      const restore = restoreRef.current;
      if (restore?.isConnected) {
        restore.focus();
      } else {
        // A nested confirmation can finish by replacing its trigger (for
        // example, an editor becomes a detail view). Keep focus in the
        // still-open parent modal when the original element is gone.
        const parentDialog = Array.from(document.querySelectorAll<HTMLDialogElement>("dialog[open]")).find(
          (candidate) => candidate !== dialog,
        );
        if (parentDialog) focusFirst(parentDialog);
      }
      restoreRef.current = null;
    };
  }, [open, initialFocusRef]);

  if (!open) {
    return null;
  }

  const handleKeyDown = (event: React.KeyboardEvent<HTMLDialogElement>) => {
    if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      onClose();
      return;
    }
    if (event.key !== "Tab") {
      return;
    }
    const dialog = dialogRef.current;
    const focusables = dialog?.querySelectorAll<HTMLElement>(FOCUSABLE);
    if (!focusables || focusables.length === 0) {
      event.preventDefault();
      dialog?.focus();
      return;
    }
    const first = focusables[0];
    const last = focusables[focusables.length - 1];
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  };

  return (
    <dialog
      ref={dialogRef}
      aria-labelledby={titleId}
      aria-describedby={description ? descriptionId : undefined}
      aria-modal="true"
      data-testid="dialog-backdrop"
      tabIndex={-1}
      className={`fixed inset-0 z-50 m-0 flex h-full max-h-none w-full max-w-none items-center justify-center border-0 bg-transparent p-2 outline-none backdrop:bg-ink/40 md:p-6 ${danger ? "border-t-4 border-t-accent" : ""}`}
      onCancel={(event) => {
        event.preventDefault();
        onClose();
      }}
      onClick={(event) => {
        if (dismissible && event.target === event.currentTarget) onClose();
      }}
      onKeyDown={handleKeyDown}
    >
      <div
        role="document"
        className={`flex max-h-[calc(100dvh-16px)] w-full max-w-3xl min-w-0 flex-col overflow-hidden border-2 border-ink bg-paper md:max-h-[calc(100dvh-48px)] ${danger ? "border-t-4 border-t-accent" : ""}`}
        onClick={(event) => event.stopPropagation()}
      >
        <header className="flex shrink-0 items-start justify-between gap-4 border-b border-ink px-4 py-3 md:px-6">
          <div className="min-w-0 flex-1">
            <h2 id={titleId} className="font-display text-2xl font-bold md:text-3xl">
              {title}
            </h2>
            {description && (
              <div id={descriptionId} className="mt-2 font-body text-sm leading-relaxed text-neutral-600">
                {description}
              </div>
            )}
          </div>
          {dismissible && (
            <button
              type="button"
              aria-label="关闭弹窗"
              className="flex min-h-[44px] min-w-[44px] shrink-0 items-center justify-center border border-ink font-mono text-xl leading-none hover:bg-ink hover:text-paper focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ink"
              onClick={onClose}
            >
              ×
            </button>
          )}
        </header>
        <div data-testid="dialog-body" className="min-h-0 flex-1 overflow-y-auto overflow-x-hidden px-4 py-4 md:px-6 md:py-6">
          {children}
        </div>
        {footer && <footer className="shrink-0 border-t border-ink px-4 py-4 md:px-6">{footer}</footer>}
      </div>
    </dialog>
  );
}

/**
 * Confirmation dialog kept source-compatible with existing callers while
 * sharing the modal's focus, stacking, and scroll-lock behavior.
 */
export function ConfirmDialog({
  open,
  title,
  description,
  confirmLabel = "确认",
  cancelLabel = "取消",
  confirmDisabled = false,
  danger,
  onConfirm,
  onCancel,
}: ConfirmDialogProps) {
  return (
    <Dialog
      open={open}
      title={title}
      description={description}
      danger={danger}
      dismissible={false}
      onClose={onCancel}
      footer={
        <div className="flex justify-end gap-3">
          <Button variant="secondary" onClick={onCancel}>
            {cancelLabel}
          </Button>
          <Button variant={danger ? "danger" : "primary"} disabled={confirmDisabled} onClick={onConfirm}>
            {confirmLabel}
          </Button>
        </div>
      }
    >
      <span className="sr-only">请在下方选择操作。</span>
    </Dialog>
  );
}
