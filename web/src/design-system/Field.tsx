import type { InputHTMLAttributes } from "react";

const inputClasses =
  "w-full min-h-[44px] border-b-2 border-ink bg-transparent px-3 py-2 font-mono text-sm " +
  "focus-visible:bg-neutral-100 focus-visible:outline-none";

export type FieldProps = InputHTMLAttributes<HTMLInputElement> & {
  /** Persistent visible label (never placeholder-only). */
  label: string;
  /** Stable id; also keys the error/hint aria wiring. */
  id: string;
  hint?: string;
  error?: string | null;
  /** Hide the label visually while keeping it for assistive tech. */
  hideLabel?: boolean;
};

/**
 * Newsprint field: bottom-border input, monospace value, persistent label
 * and a persistent error slot so layout never shifts when validation fires.
 */
export function Field({ label, id, hint, error, hideLabel, className, ...input }: FieldProps) {
  const hintId = hint ? `${id}-hint` : undefined;
  const errorId = error ? `${id}-error` : undefined;
  const describedBy = [hintId, errorId].filter(Boolean).join(" ") || undefined;
  return (
    <div className="space-y-1">
      <label
        htmlFor={id}
        className={`block font-mono text-xs uppercase tracking-widest ${hideLabel ? "sr-only" : ""}`}
      >
        {label}
      </label>
      <input
        id={id}
        name={id}
        aria-describedby={describedBy}
        aria-invalid={error ? true : undefined}
        className={`${inputClasses}${error ? " border-accent" : ""}${className ? ` ${className}` : ""}`}
        {...input}
      />
      {hint && (
        <p id={hintId} className="font-body text-xs text-neutral-500">
          {hint}
        </p>
      )}
      {error && (
        <p id={errorId} className="font-body text-xs text-accent">
          {error}
        </p>
      )}
    </div>
  );
}
