import type { ButtonHTMLAttributes } from "react";

export type ButtonVariant = "primary" | "secondary" | "ghost" | "link";

const variantClasses: Record<ButtonVariant, string> = {
  primary: "border border-transparent bg-ink text-paper hover:border-ink hover:bg-paper hover:text-ink",
  secondary: "border border-ink bg-transparent text-ink hover:bg-ink hover:text-paper",
  ghost: "text-ink hover:bg-divider",
  link: "text-ink decoration-accent decoration-2 underline-offset-4 hover:underline",
};

export type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: ButtonVariant;
};

/**
 * Newsprint button: sharp corners, uppercase tracked label, inverted-color
 * hover, and a 44px minimum touch target on every variant.
 */
export function Button({ variant = "primary", className = "", type = "button", ...rest }: ButtonProps) {
  const classes = [
    "inline-flex min-h-[44px] min-w-[44px] items-center justify-center px-6 py-2.5",
    "text-xs font-semibold uppercase tracking-widest transition-all duration-200",
    "focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ink",
    "disabled:cursor-not-allowed disabled:opacity-50",
    variantClasses[variant],
    className,
  ].join(" ");
  return <button type={type} className={classes} {...rest} />;
}
