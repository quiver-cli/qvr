import type { ReactNode } from "react";

type Variant = "default" | "raised" | "inset" | "accent";

// Card — 1px border, 10px radius, header/body/footer slots. `accent` adds the
// lime border + soft glow (reserved for the one emphasized surface per view).
export function Card({
  variant = "default",
  title,
  actions,
  footer,
  className,
  children,
}: {
  variant?: Variant;
  title?: ReactNode;
  actions?: ReactNode;
  footer?: ReactNode;
  className?: string;
  children: ReactNode;
}) {
  const cls = [
    "qvr-card",
    variant !== "default" ? `qvr-card--${variant}` : "",
    className ?? "",
  ]
    .filter(Boolean)
    .join(" ");
  return (
    <section className={cls}>
      {(title != null || actions != null) && (
        <div className="qvr-card__header">
          {title != null && <h3 className="qvr-card__title">{title}</h3>}
          {actions != null && <div style={{ marginLeft: "auto" }}>{actions}</div>}
        </div>
      )}
      <div className="qvr-card__body">{children}</div>
      {footer != null && <div className="qvr-card__footer">{footer}</div>}
    </section>
  );
}
