import type { ReactNode } from "react";
import { NavLink } from "react-router-dom";
import {
  GitCommitHorizontal,
  Layers,
  LayoutDashboard,
  Library,
  MessagesSquare,
  Package,
} from "lucide-react";
import ScopeSwitcher from "./ScopeSwitcher";
import { QuiverMark } from "../assets/QuiverMark";
import { api, useFetch, type Scope } from "../api";

// Shell — the DS dashboard composition: always-dark 240px sidebar over the
// ink ramp, light "paper" content area (data-theme swap on <main>), toast
// viewport handled by ToastProvider. Registries are global (above the project
// switcher); the rest of the nav answers through the selected scope.

const scopedNav = [
  { to: "/", label: "Overview", end: true, icon: <LayoutDashboard /> },
  { to: "/sessions", label: "Sessions", icon: <MessagesSquare /> },
  { to: "/skills", label: "Skills", icon: <Package /> },
  { to: "/deadweight", label: "Dead weight", icon: <Layers /> },
  { to: "/provenance", label: "Provenance", icon: <GitCommitHorizontal /> },
];

// Active state rides on the aria-current="page" attribute react-router sets;
// extensions.css maps it onto the kit's [data-active] visuals.
function NavItem({
  to,
  label,
  icon,
  end,
}: {
  to: string;
  label: string;
  icon: ReactNode;
  end?: boolean;
}) {
  return (
    <NavLink to={to} end={end} className="qvr-navitem">
      <span className="qvr-navitem__bar" />
      {icon}
      {label}
    </NavLink>
  );
}

export default function Shell({
  children,
  scope,
  onScopeChange,
  wide = false,
}: {
  children: ReactNode;
  scope: Scope;
  onScopeChange: (s: Scope) => void;
  wide?: boolean;
}) {
  const { data: health } = useFetch(api.health, "health");
  return (
    <div className="qvr-app">
      <aside className="qvr-side">
        <div className="qvr-side__brand">
          <span style={{ color: "var(--brand-500)", display: "inline-flex" }}>
            <QuiverMark size={20} />
          </span>
          <span className="qvr-side__word">quiver</span>
          <span className="qvr-side__badge">ui</span>
        </div>

        <div className="qvr-side__nav">
          <NavItem to="/registries" label="Registries" icon={<Library />} />
        </div>

        <div className="qvr-side__group">
          <span className="qvr-side__label">project</span>
          <ScopeSwitcher scope={scope} onChange={onScopeChange} />
        </div>

        <div className="qvr-side__nav">
          {scopedNav.map((n) => (
            <NavItem key={n.to} to={n.to} label={n.label} icon={n.icon} end={n.end} />
          ))}
        </div>

        <div className="qvr-side__foot">
          <span className="qvr-side__dot" />
          <span>read-only · local{health?.version ? ` · ${health.version}` : ""}</span>
        </div>
      </aside>
      <main className="qvr-main" data-theme="light">
        <div className={`qvr-main__inner${wide ? " qvr-main__inner--wide" : ""}`}>{children}</div>
      </main>
    </div>
  );
}
