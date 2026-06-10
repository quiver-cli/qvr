import type { InputHTMLAttributes, ReactNode, SelectHTMLAttributes } from "react";
import { ChevronDown } from "lucide-react";

// Input — inset mono field with optional leading icon/affix and lime focus glow.
export function Input({
  icon,
  affix,
  invalid = false,
  wrapClassName,
  ...rest
}: {
  icon?: ReactNode;
  affix?: ReactNode;
  invalid?: boolean;
  wrapClassName?: string;
} & InputHTMLAttributes<HTMLInputElement>) {
  const cls = ["qvr-input-wrap", invalid ? "qvr-input-wrap--invalid" : "", wrapClassName ?? ""]
    .filter(Boolean)
    .join(" ");
  return (
    <span className={cls}>
      {icon}
      {affix != null && <span className="qvr-affix">{affix}</span>}
      <input className="qvr-input" {...rest} />
    </span>
  );
}

// Field wraps a labelled control (eyebrow-style uppercase label).
export function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <label className="qvr-field">
      <span className="qvr-label">{label}</span>
      {children}
    </label>
  );
}

// Select — native select in DS chrome with the chevron glyph.
export function Select({
  children,
  ...rest
}: SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <span className="qvr-select-wrap">
      <select className="qvr-select" {...rest}>
        {children}
      </select>
      <ChevronDown />
    </span>
  );
}
