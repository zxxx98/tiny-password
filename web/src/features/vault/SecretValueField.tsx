import { useEffect, useRef, useState } from "react";
import { Button } from "../../design-system/Button";
import { scheduleClipboardCleanup } from "./clipboardCleanup";

export function SecretValueField({ value, index }: { value: string; index: number }) {
  const [revealed, setRevealed] = useState(false);
  const [copyState, setCopyState] = useState<string | null>(null);
  const mountedRef = useRef(true);
  const valueRef = useRef(value);
  valueRef.current = value;

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  useEffect(() => {
    setRevealed(false);
  }, [value]);

  const copy = async () => {
    const copiedValue = valueRef.current;
    try {
      await navigator.clipboard.writeText(copiedValue);
    } catch {
      setCopyState("复制失败，请重试或手动复制");
      return;
    }
    setCopyState("已复制。30 秒后将尽力清除剪贴板；若浏览器不允许则无法清除。");
    void scheduleClipboardCleanup(copiedValue).then((result) => {
      if (!mountedRef.current) return;
      if (result === "cleared") setCopyState("剪贴板已清除。");
      if (result === "changed") setCopyState("剪贴板内容已被替换，未执行清理。");
      if (result === "unavailable") setCopyState("浏览器不允许读取剪贴板，无法自动清除，请手动处理。");
    });
  };

  return (
    <div className="space-y-1">
      <div className="flex flex-wrap items-center gap-2">
        {revealed ? (
          <output data-testid={`secret-value-${index}`} className="min-h-[44px] flex-1 break-all whitespace-pre-wrap border-b-2 border-ink px-3 py-2 font-mono text-sm">
            {value}
          </output>
        ) : (
          <span data-testid={`secret-masked-${index}`} aria-label={`值第 ${index + 1} 组（已遮蔽）`} className="min-h-[44px] flex-1 select-none px-3 py-2 font-mono text-sm tracking-widest text-neutral-500">
            ••••••••
          </span>
        )}
        <Button variant="secondary" aria-label={`${revealed ? "隐藏" : "显示"}第 ${index + 1} 组`} onClick={() => setRevealed((current) => !current)}>
          {revealed ? "隐藏" : "显示"}
        </Button>
        <Button variant="ghost" aria-label={`复制第 ${index + 1} 组`} onClick={() => void copy()}>
          复制
        </Button>
      </div>
      {copyState && <p role="status" className="font-body text-xs text-neutral-600">{copyState}</p>}
    </div>
  );
}
