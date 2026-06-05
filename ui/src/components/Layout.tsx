import type { ReactNode } from "react";
import { NavLink } from "react-router-dom";
import ScopeSwitcher from "./ScopeSwitcher";
import type { Scope } from "../api";

// Dashboard chrome: a dark left sidebar of sections + a light content area.
// Registries are global (top, above the project switcher); the rest of the nav
// is scoped to whichever project the switcher has selected.

const scopedNav = [
  { to: "/", label: "Overview", end: true },
  { to: "/sessions", label: "Sessions" },
  { to: "/skills", label: "Skills" },
  { to: "/provenance", label: "Provenance" },
];

const navClass = ({ isActive }: { isActive: boolean }) =>
  `block rounded-lg px-3 py-2 text-sm font-medium transition ${
    isActive ? "bg-gray-800 text-white" : "text-gray-400 hover:bg-gray-800/60 hover:text-gray-200"
  }`;

export default function Layout({
  children,
  scope,
  onScopeChange,
}: {
  children: ReactNode;
  scope: Scope;
  onScopeChange: (s: Scope) => void;
}) {
  return (
    <div className="flex min-h-full bg-gray-50 text-gray-900">
      <aside className="flex w-56 shrink-0 flex-col border-r border-gray-800 bg-gray-900 text-gray-300">
        <div className="flex items-center gap-2 px-5 py-5">
          <span className="text-lg font-semibold text-white">quiver</span>
          <span className="rounded bg-gray-700 px-1.5 py-0.5 text-[0.625rem] font-medium text-gray-300">
            ui
          </span>
        </div>

        {/* Global section — spans all projects. */}
        <nav className="space-y-1 px-3">
          <NavLink to="/registries" className={navClass}>
            Registries
          </NavLink>
        </nav>

        {/* Project switcher + scoped pages. */}
        <div className="mt-4 px-3 text-[0.625rem] font-semibold uppercase tracking-wide text-gray-600">
          Project
        </div>
        <div className="mt-1">
          <ScopeSwitcher scope={scope} onChange={onScopeChange} />
        </div>
        <nav className="flex-1 space-y-1 px-3">
          {scopedNav.map((n) => (
            <NavLink key={n.to} to={n.to} end={n.end} className={navClass}>
              {n.label}
            </NavLink>
          ))}
        </nav>

        <div className="px-5 py-4 text-[0.6875rem] leading-relaxed text-gray-500">
          read-only · local
          <br />
          uv for agent skills
        </div>
      </aside>
      <main className="flex-1 overflow-x-hidden">
        <div className="mx-auto max-w-6xl px-8 py-8">{children}</div>
      </main>
    </div>
  );
}
