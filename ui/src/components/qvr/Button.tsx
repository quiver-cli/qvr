import type { ButtonHTMLAttributes, ReactNode } from "react";

type Variant = "primary" | "secondary" | "ghost" | "outline" | "danger";
type Size = "sm" | "md" | "lg";

// Button — mono label, 7px radius. One lime `primary` per view.
export function Button({
  variant = "secondary",
  size = "md",
  block = false,
  leftIcon,
  rightIcon,
  className,
  children,
  ...rest
}: {
  variant?: Variant;
  size?: Size;
  block?: boolean;
  leftIcon?: ReactNode;
  rightIcon?: ReactNode;
} & ButtonHTMLAttributes<HTMLButtonElement>) {
  const cls = [
    "qvr-btn",
    variant !== "secondary" ? `qvr-btn--${variant}` : "",
    size !== "md" ? `qvr-btn--${size}` : "",
    block ? "qvr-btn--block" : "",
    className ?? "",
  ]
    .filter(Boolean)
    .join(" ");
  return (
    <button type="button" className={cls} {...rest}>
      {leftIcon}
      {children}
      {rightIcon}
    </button>
  );
}
