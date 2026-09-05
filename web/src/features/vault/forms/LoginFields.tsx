import { Button } from "../../../design-system/Button";
import { Field } from "../../../design-system/Field";
import { LIMITS, overLimit, type LoginPayload } from "../types";

export type FieldsProps<P> = {
  payload: P;
  errors: Record<string, string>;
  onChange: (patch: Partial<P>) => void;
  disabled?: boolean;
};

export function validateLogin(p: LoginPayload): Record<string, string> {
  const errors: Record<string, string> = {};
  if (!p.name.trim()) errors.name = "名称必填。";
  else {
    const over = overLimit(p.name, LIMITS.name);
    if (over) errors.name = over;
  }
  if (!p.username?.trim()) errors.username = "用户名必填。";
  if (!p.password) errors.password = "密码必填。";
  else if (new TextEncoder().encode(p.password).length > 1024) errors.password = "密码最多 1024 字节（D09）。";
  if ((p.urls?.length ?? 0) > LIMITS.urls) errors.urls = `最多 ${LIMITS.urls} 个网址。`;
  for (const [i, u] of (p.urls ?? []).entries()) {
    const over = overLimit(u, LIMITS.url);
    if (over) errors.urls = `第 ${i + 1} 个网址${over}`;
  }
  const notes = overLimit(p.notes, LIMITS.notes);
  if (notes) errors.notes = notes;
  for (const key of ["password_updated_at", "password_expires_at"] as const) {
    const value = p[key];
    if (value && !/^\d{4}-\d{2}-\d{2}$/.test(value)) errors[key] = "日期格式应为 YYYY-MM-DD。";
  }
  return errors;
}

export function LoginFields({ payload, errors, onChange, disabled }: FieldsProps<LoginPayload>) {
  return (
    <>
      <Field id="f-name" label="名称" required value={payload.name} error={errors.name} disabled={disabled}
        onChange={(e) => onChange({ name: e.target.value })} />
      <Field id="f-username" label="用户名" value={payload.username ?? ""} error={errors.username} disabled={disabled}
        onChange={(e) => onChange({ username: e.target.value })} />
      <Field id="f-password" label="密码" type="password" autoComplete="off" value={payload.password ?? ""} error={errors.password} disabled={disabled}
        onChange={(e) => onChange({ password: e.target.value })} />
      <div className="space-y-2">
        <span className="block font-mono text-xs uppercase tracking-widest">网址</span>
        {(payload.urls ?? []).map((url, i) => (
          <div key={i} className="flex gap-2">
            <Field id={`f-url-${i}`} label={`网址 ${i + 1}`} hideLabel value={url} disabled={disabled}
              onChange={(e) => {
                const urls = [...(payload.urls ?? [])];
                urls[i] = e.target.value;
                onChange({ urls });
              }} />
            <Button variant="ghost" aria-label={`删除网址 ${i + 1}`}
              onClick={() => onChange({ urls: (payload.urls ?? []).filter((_, j) => j !== i) })}>
              删除
            </Button>
          </div>
        ))}
        <Button variant="secondary" onClick={() => onChange({ urls: [...(payload.urls ?? []), ""] })}>
          添加网址
        </Button>
        {errors.urls && <p className="font-body text-xs text-accent">{errors.urls}</p>}
      </div>
      <Field id="f-notes" label="备注" value={payload.notes ?? ""} error={errors.notes} disabled={disabled}
        onChange={(e) => onChange({ notes: e.target.value })} />
      <Field id="f-pw-updated" label="密码更新日期" type="date" value={payload.password_updated_at ?? ""} error={errors.password_updated_at}
        onChange={(e) => onChange({ password_updated_at: e.target.value || null })} />
      <Field id="f-pw-expires" label="密码过期日期" type="date" value={payload.password_expires_at ?? ""} error={errors.password_expires_at}
        onChange={(e) => onChange({ password_expires_at: e.target.value || null })} />
    </>
  );
}
