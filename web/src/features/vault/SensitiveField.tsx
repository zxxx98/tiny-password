import { useCallback, useEffect, useRef, useState } from "react";
import { request } from "../../app/api";
import { Button } from "../../design-system/Button";

/**
 * SensitiveField renders one secret (design §6.4): masked by default, shown
 * only after an explicit, audited reveal, re-masked after 30 seconds or on
 * blur, and copied through the clipboard with an honest best-effort cleanup
 * 30 seconds later. The value never sits in a focusable plaintext input
 * while masked — masking is not CSS-only.
 */
export function SensitiveField({
  label,
  value,
  field,
  itemId,
  csrfToken,
}: {
  label: string;
  value: string;
  field: string;
  itemId: string;
  csrfToken: string;
}) {
  const [revealed, setRevealed] = useState(false);
  const [copyState, setCopyState] = useState<string | null>(null);
  const hideTimer = useRef<number | undefined>(undefined);
  const clipboardTimer = useRef<number | undefined>(undefined);
  const valueRef = useRef(value);
  valueRef.current = value;

  const clearTimers = useCallback(() => {
    window.clearTimeout(hideTimer.current);
    window.clearTimeout(clipboardTimer.current);
  }, []);

  useEffect(() => clearTimers, [clearTimers]);
  // Any value change re-masks immediately: a stale reveal never lingers.
  useEffect(() => {
    setRevealed(false);
  }, [value]);

  const audit = useCallback(
    async (kind: "reveal" | "copy") => {
      try {
        await request("POST", `/api/v1/items/${itemId}/${kind}`, { field }, { csrfToken });
      } catch {
        // The audit write failing never blocks the interaction's other half.
      }
    },
    [itemId, field, csrfToken],
  );

  const reveal = useCallback(() => {
    setRevealed(true);
    void audit("reveal");
    clearTimers();
    hideTimer.current = window.setTimeout(() => setRevealed(false), 30_000);
  }, [audit, clearTimers]);

  const mask = useCallback(() => {
    setRevealed(false);
    clearTimers();
  }, [clearTimers]);

  const copy = useCallback(async () => {
    try {
      await navigator.clipboard.writeText(valueRef.current);
    } catch {
      setCopyState("浏览器不允许写入剪贴板，请手动选择并复制。");
      void audit("copy");
      return;
    }
    void audit("copy");
    setCopyState("已复制。30 秒后将尽力清除剪贴板；若浏览器不允许则无法清除。");
    clearTimers();
    clipboardTimer.current = window.setTimeout(async () => {
      try {
        const current = await navigator.clipboard.readText();
        if (current === valueRef.current) {
          await navigator.clipboard.writeText("");
        }
        setCopyState("剪贴板已清除。");
      } catch {
        setCopyState("浏览器不允许读取剪贴板，无法自动清除，请手动处理。");
      }
    }, 30_000);
  }, [audit, clearTimers]);

  return (
    <div className="space-y-1">
      <span className="block font-mono text-xs uppercase tracking-widest">{label}</span>
      <div className="flex flex-wrap items-center gap-2">
        {revealed ? (
          <output
            data-testid={`secret-value-${field}`}
            tabIndex={0}
            onBlur={mask}
            className="min-h-[44px] flex-1 break-all border-b-2 border-ink bg-transparent px-3 py-2 font-mono text-sm"
          >
            {value}
          </output>
        ) : (
          <span
            data-testid={`secret-masked-${field}`}
            aria-label={`${label}（已遮蔽）`}
            className="min-h-[44px] flex-1 select-none px-3 py-2 font-mono text-sm tracking-widest text-neutral-500"
          >
            ••••••••
          </span>
        )}
        {revealed ? (
          <Button variant="secondary" onClick={mask}>
            遮蔽
          </Button>
        ) : (
          <Button variant="secondary" onClick={reveal}>
            显示
          </Button>
        )}
        <Button variant="ghost" onClick={() => void copy()}>
          复制
        </Button>
      </div>
      {copyState && (
        <p role="status" className="font-body text-xs text-neutral-600">
          {copyState}
        </p>
      )}
    </div>
  );
}
