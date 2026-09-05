import { Field } from "../../../design-system/Field";
import { LIMITS, overLimit, type SecureNotePayload } from "../types";
import type { FieldsProps } from "./LoginFields";

export function validateSecureNote(p: SecureNotePayload): Record<string, string> {
  const errors: Record<string, string> = {};
  if (!p.name.trim()) errors.name = "标题必填。";
  else {
    const over = overLimit(p.name, LIMITS.name);
    if (over) errors.name = over;
  }
  if (!p.body.trim()) errors.body = "正文必填。";
  else {
    const over = overLimit(p.body, LIMITS.noteBody);
    if (over) errors.body = over;
  }
  return errors;
}

export function SecureNoteFields({ payload, errors, onChange, disabled }: FieldsProps<SecureNotePayload>) {
  return (
    <>
      <Field id="f-name" label="标题" required value={payload.name} error={errors.name} disabled={disabled}
        onChange={(e) => onChange({ name: e.target.value })} />
      <div className="space-y-1">
        <label htmlFor="f-body" className="block font-mono text-xs uppercase tracking-widest">正文</label>
        <textarea id="f-body" rows={8} value={payload.body} disabled={disabled}
          onChange={(e) => onChange({ body: e.target.value })}
          className="w-full border-b-2 border-ink bg-transparent px-3 py-2 font-mono text-sm focus-visible:bg-neutral-100 focus-visible:outline-none" />
        {errors.body && <p className="font-body text-xs text-accent">{errors.body}</p>}
      </div>
    </>
  );
}
