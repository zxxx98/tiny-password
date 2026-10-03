import { lazy, Suspense, useEffect, useRef, useState } from "react";
import { request } from "../../app/api";
import { Button } from "../../design-system/Button";
import { Field } from "../../design-system/Field";
import { errorText } from "./types";

const PasswordStrength = lazy(() => import("./PasswordStrength"));

export function PasswordTools({ value, username, name, csrfToken, disabled, onChange, onBusyChange }: {
  value: string; username?: string; name?: string; csrfToken?: string;
  disabled?: boolean; onChange: (value: string) => void;
  onBusyChange?: (busy: boolean) => void;
}) {
  const [open, setOpen] = useState(false);
  const [length, setLength] = useState(20);
  const [options, setOptions] = useState({ lowercase: true, uppercase: true, digits: true, symbols: true, exclude_ambiguous: false });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const controllerRef = useRef<AbortController | null>(null);
  useEffect(() => () => controllerRef.current?.abort(), []);
  const generate = async () => {
    if (busy) return;
    const controller = new AbortController();
    controllerRef.current = controller;
    setBusy(true); onBusyChange?.(true); setError("");
    try {
      const result = await request<{ value: string }>("POST", "/api/v1/generators/password", { length, ...options }, { csrfToken, signal: controller.signal });
      if (!controller.signal.aborted) onChange(result.value);
    } catch (err) {
      if (!controller.signal.aborted) setError(errorText(err).message);
    } finally {
      if (!controller.signal.aborted) { setBusy(false); onBusyChange?.(false); }
    }
  };
  return <div className="space-y-2">
    {value && <Suspense fallback={<p className="font-body text-xs">正在估计密码强度…</p>}><PasswordStrength value={value} username={username} name={name} /></Suspense>}
    <Button variant="secondary" aria-expanded={open} disabled={disabled || busy} onClick={() => setOpen(!open)}>生成密码</Button>
    {open && <fieldset className="space-y-3 border border-divider p-3" disabled={disabled || busy}>
      <legend className="font-body text-sm">密码生成选项</legend>
      <Field id="inline-password-length" label="密码长度" type="number" min={8} max={128} value={length} onChange={(event) => setLength(Number(event.target.value))} />
      <div className="flex flex-wrap gap-3">
        {([["lowercase", "小写字母"], ["uppercase", "大写字母"], ["digits", "数字"], ["symbols", "符号"], ["exclude_ambiguous", "排除易混淆字符"]] as const).map(([key, label]) => <label key={key} className="flex min-h-[44px] items-center gap-2 font-body text-sm">
          <input type="checkbox" checked={options[key]} onChange={(event) => setOptions({ ...options, [key]: event.target.checked })} />{label}
        </label>)}
      </div>
      <p className="font-body text-xs text-neutral-500">生成后替换当前密码输入，保存条目后才生效。</p>
      <Button disabled={busy || !Number.isInteger(length) || length < 8 || length > 128 || !(options.lowercase || options.uppercase || options.digits || options.symbols)} onClick={() => void generate()}>{busy ? "生成中…" : "生成并填入"}</Button>
      {error && <p role="alert" className="font-body text-sm text-accent">{error}</p>}
    </fieldset>}
  </div>;
}
