import { useState } from "react";
import { Button } from "../../../design-system/Button";
import { Field } from "../../../design-system/Field";
import { LIMITS, overLimit, type SecretPayload } from "../types";
import type { FieldsProps } from "./LoginFields";

const inputClasses =
  "min-h-[44px] w-full border-b-2 border-ink bg-transparent px-3 py-2 font-mono text-sm focus-visible:bg-neutral-100 focus-visible:outline-none";

// Secret values may contain newlines. Keep the password type marker for
// password-manager/accessibility integrations while using a textarea so the
// editor can preserve the complete value.
const valueTypeMarker = (visible: boolean) => ({
  type: visible ? "text" : "password",
} as React.TextareaHTMLAttributes<HTMLTextAreaElement>);

export function validateSecret(payload: SecretPayload): Record<string, string> {
  const errors: Record<string, string> = {};
  if (!payload.name.trim()) errors.name = "标题必填。";
  else {
    const over = overLimit(payload.name, LIMITS.name);
    if (over) errors.name = over;
  }
  if (payload.entries.length < 1) errors.entries = "至少需要 1 组。";
  if (payload.entries.length > LIMITS.secretEntries) errors.entries = `最多 ${LIMITS.secretEntries} 组。`;

  const seen = new Set<string>();
	payload.entries.forEach((entry, index) => {
		const key = `entries.${index}.key`;
		const value = `entries.${index}.value`;
		if (!entry.key.trim()) {
			errors[key] = "键必填。";
		} else {
			const keyOver = overLimit(entry.key, LIMITS.secretKey);
			if (keyOver) errors[key] = keyOver;
			else if (seen.has(entry.key)) errors[key] = "键不能重复。";
			else seen.add(entry.key);
		}
    const valueOver = overLimit(entry.value, LIMITS.secretValue);
    if (valueOver) errors[value] = valueOver;
  });
  const notesOver = overLimit(payload.notes, LIMITS.notes);
  if (notesOver) errors.notes = notesOver;
  return errors;
}

export function SecretFields({ payload, errors, onChange, disabled }: FieldsProps<SecretPayload>) {
  const [visible, setVisible] = useState<Record<number, boolean>>({});

  const updateEntry = (index: number, patch: Partial<SecretPayload["entries"][number]>) => {
    const entries = payload.entries.map((entry, entryIndex) => entryIndex === index ? { ...entry, ...patch } : entry);
    onChange({ entries });
  };

  const removeEntry = (index: number) => {
    onChange({ entries: payload.entries.filter((_, entryIndex) => entryIndex !== index) });
    setVisible((current) => Object.fromEntries(
      Object.entries(current)
        .filter(([entryIndex]) => Number(entryIndex) !== index)
        .map(([entryIndex, isVisible]) => [Number(entryIndex) > index ? Number(entryIndex) - 1 : Number(entryIndex), isVisible]),
    ));
  };

  return (
    <>
      <Field id="f-name" label="标题" required value={payload.name} error={errors.name} disabled={disabled}
        onChange={(event) => onChange({ name: event.target.value })} />
      <fieldset className="space-y-4">
        <legend className="font-mono text-xs uppercase tracking-widest">键值组</legend>
		{payload.entries.map((entry, index) => {
			const keyError = errors[`entries.${index}.key`];
			const valueError = errors[`entries.${index}.value`];
			const isVisible = visible[index] === true;
			const maskedValue = Array.from(entry.value, (character) =>
				character === "\n" || character === "\r" ? character : "•").join("") || "••••••••";
			return (
            <div key={index} className="space-y-2 border-l-2 border-divider pl-3">
              <div className="flex items-end gap-2">
                <div className="min-w-0 flex-1 space-y-1">
                  <label htmlFor={`secret-key-${index}`} className="block font-mono text-xs uppercase tracking-widest">键 {index + 1}</label>
                  <input id={`secret-key-${index}`} value={entry.key} disabled={disabled}
                    aria-invalid={keyError ? true : undefined} className={`${inputClasses}${keyError ? " border-accent" : ""}`}
                    onChange={(event) => updateEntry(index, { key: event.target.value })} />
                  {keyError && <p className="font-body text-xs text-accent">{keyError}</p>}
                </div>
                <Button variant="ghost" aria-label={`删除第 ${index + 1} 组`} disabled={disabled || payload.entries.length === 1}
                  onClick={() => removeEntry(index)}>删除</Button>
              </div>
              <div className="flex items-end gap-2">
                <div className="min-w-0 flex-1 space-y-1">
                  <label htmlFor={`secret-value-${index}`} className="block font-mono text-xs uppercase tracking-widest">值 {index + 1}</label>
					<div className="relative min-w-0 flex-1">
						<textarea id={`secret-value-${index}`} {...valueTypeMarker(isVisible)} rows={3} autoComplete="off" value={entry.value} disabled={disabled}
							aria-invalid={valueError ? true : undefined} className={`${inputClasses}${valueError ? " border-accent" : ""}${isVisible ? "" : " text-transparent"}`}
							style={{ WebkitTextSecurity: isVisible ? "none" : "disc", caretColor: "#161616" } as React.CSSProperties}
							onChange={(event) => updateEntry(index, { value: event.target.value })} />
						{!isVisible && (
							<span aria-hidden="true" className="pointer-events-none absolute inset-0 overflow-hidden whitespace-pre-wrap break-all px-3 py-2 font-mono text-sm text-neutral-500">
								{maskedValue}
							</span>
						)}
					</div>
                  {valueError && <p className="font-body text-xs text-accent">{valueError}</p>}
                </div>
                <Button variant="secondary" aria-label={`${isVisible ? "隐藏" : "显示"}第 ${index + 1} 组`} disabled={disabled}
                  onClick={() => setVisible((current) => ({ ...current, [index]: !isVisible }))}>
                  {isVisible ? "隐藏" : "显示"}
                </Button>
              </div>
            </div>
          );
        })}
        {errors.entries && <p className="font-body text-xs text-accent">{errors.entries}</p>}
        <Button variant="secondary" disabled={disabled || payload.entries.length >= LIMITS.secretEntries}
          onClick={() => onChange({ entries: [...payload.entries, { key: "", value: "" }] })}>
          添加一组
        </Button>
      </fieldset>
      <Field id="f-notes" label="备注" value={payload.notes ?? ""} error={errors.notes} disabled={disabled}
        onChange={(event) => onChange({ notes: event.target.value })} />
    </>
  );
}
