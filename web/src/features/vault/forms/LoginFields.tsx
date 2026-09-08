import { useState } from "react";
import { request } from "../../../app/api";
import { Button } from "../../../design-system/Button";
import { Field } from "../../../design-system/Field";
import { LIMITS, errorText, overLimit, type LoginPayload } from "../types";

export type FieldsProps<P> = {
  payload: P;
  errors: Record<string, string>;
  onChange: (patch: Partial<P>) => void;
  disabled?: boolean;
  csrfToken?: string;
};

type LoginProbeCandidate = {
  url: string;
  score: number;
  password_field: boolean;
  reasons: string[];
};

type LoginProbeResponse = {
  input_url: string;
  candidates: LoginProbeCandidate[];
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

export function LoginFields({ payload, errors, onChange, disabled, csrfToken }: FieldsProps<LoginPayload>) {
  const [probingIndex, setProbingIndex] = useState<number | null>(null);
  const [probeState, setProbeState] = useState<{ index: number; candidates: LoginProbeCandidate[]; message?: string } | null>(null);

  const probe = async (index: number, value: string) => {
    if (!value.trim()) {
      setProbeState({ index, candidates: [], message: "请先填写网址。" });
      return;
    }
    setProbingIndex(index);
    setProbeState(null);
    try {
      const result = await request<LoginProbeResponse>(
        "POST",
        "/api/v1/items/login-page-probe",
        { url: value },
        { csrfToken },
      );
      setProbeState({ index, candidates: result.candidates ?? [] });
    } catch (err) {
      setProbeState({ index, candidates: [], message: errorText(err).message || "暂时无法探测该网址。" });
    } finally {
      setProbingIndex(null);
    }
  };

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
          <div key={i} className="space-y-2">
            <div className="flex gap-2">
              <Field id={`f-url-${i}`} label={`网址 ${i + 1}`} hideLabel value={url} disabled={disabled || probingIndex !== null}
                onChange={(e) => {
                  const urls = [...(payload.urls ?? [])];
                  urls[i] = e.target.value;
                  if (probeState?.index === i) setProbeState(null);
                  onChange({ urls });
                }} />
              <Button variant="ghost" aria-label={`删除网址 ${i + 1}`}
                disabled={disabled || probingIndex !== null}
                onClick={() => onChange({ urls: (payload.urls ?? []).filter((_, j) => j !== i) })}>
                删除
              </Button>
            </div>
            <Button variant="secondary" disabled={disabled || probingIndex !== null} onClick={() => void probe(i, url)}>
              {probingIndex === i ? "探测中…" : "查找登录页"}
            </Button>
            {probeState?.index === i && (
              <div className="border-l-2 border-accent px-3 py-2" role="status" aria-live="polite">
                {probeState.message ? (
                  <p className="font-body text-xs text-accent">{probeState.message}</p>
                ) : probeState.candidates.length > 0 ? (
                  <>
                    <p className="font-mono text-xs uppercase tracking-widest">可能的登录页</p>
                    <ul className="mt-2 space-y-2">
                      {probeState.candidates.map((candidate) => (
                        <li key={candidate.url} className="flex flex-wrap items-center justify-between gap-2">
                          <span className="break-all font-mono text-xs">{candidate.url}</span>
                          <Button
                            variant="link"
                            className="px-0 py-0"
                            onClick={() => {
                              const urls = [...(payload.urls ?? [])];
                              urls[i] = candidate.url;
                              onChange({ urls });
                              setProbeState(null);
                            }}
                          >
                            使用此网址
                          </Button>
                        </li>
                      ))}
                    </ul>
                  </>
                ) : (
                  <p className="font-body text-xs text-neutral-600">未找到明显的登录页，你仍可以保存当前网址。</p>
                )}
              </div>
            )}
          </div>
        ))}
        <Button variant="secondary" disabled={disabled || probingIndex !== null} onClick={() => onChange({ urls: [...(payload.urls ?? []), ""] })}>
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
