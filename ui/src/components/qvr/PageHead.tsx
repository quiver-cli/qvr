import type { ReactNode } from "react";
import { ArrowLeft } from "lucide-react";
import { Link } from "react-router-dom";

// PageHead — list-page header: brand-mono h1 + sans subline, with an actions
// slot on the right (filter input, the view's one primary button).
export function PageHead({
  title,
  sub,
  actions,
}: {
  title: ReactNode;
  sub?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <div className="qvr-listhead">
      <div>
        <h1 className="qvr-h1">{title}</h1>
        {sub != null && <p className="qvr-sub">{sub}</p>}
      </div>
      {actions != null && (
        <div style={{ display: "flex", gap: 10, alignItems: "center" }}>{actions}</div>
      )}
    </div>
  );
}

// Back — the accent back-link above detail pages.
export function Back({ to, label }: { to: string; label: string }) {
  return (
    <Link to={to} className="qvr-back">
      <ArrowLeft />
      {label}
    </Link>
  );
}

// Section — titled block with the kit's spacing rhythm and an actions slot.
export function Section({
  title,
  actions,
  children,
}: {
  title: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className="qvr-section">
      <div className="qvr-cardhead">
        <h3 className="qvr-cardtitle">{title}</h3>
        {actions}
      </div>
      <div style={{ marginTop: 10 }}>{children}</div>
    </div>
  );
}

// DetailHeader — detail-page name row (mono 28px) + badges + sans description.
export function DetailHeader({
  name,
  badges,
  desc,
}: {
  name: ReactNode;
  badges?: ReactNode;
  desc?: ReactNode;
}) {
  return (
    <>
      <div className="qvr-dh">
        <h1 className="qvr-dh__name">{name}</h1>
        {badges}
      </div>
      {desc != null && <p className="qvr-dh__desc">{desc}</p>}
    </>
  );
}
