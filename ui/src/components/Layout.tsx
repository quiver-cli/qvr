import type { ReactNode } from "react";
import { NavLink } from "react-router-dom";
import ScopeSwitcher from "./ScopeSwitcher";
import type { Scope } from "../api";

// Dashboard chrome: a precise left rail of sections + a calibrated content area.
// Registries are global (top, above the project switcher); the rest of the nav
// is scoped to whichever project the switcher has selected.

const scopedNav = [
  { to: "/", label: "Overview", end: true },
  { to: "/sessions", label: "Sessions" },
  { to: "/skills", label: "Skills" },
  { to: "/provenance", label: "Provenance" },
];

const navClass = ({ isActive }: { isActive: boolean }) =>
  `block rounded-[4px] border px-3 py-2 text-sm font-medium transition ${
    isActive
      ? "border-[#92b7aa] bg-[#e7f1ed] text-[#123a2e]"
      : "border-transparent text-[#5f6d67] hover:border-[#d7ddda] hover:bg-white hover:text-[#17211d]"
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
    <div className="flex min-h-full bg-[#f7f8f8] text-[#17211d]">
      <aside className="flex w-64 shrink-0 flex-col border-r border-[#cfd6d2] bg-[#eef2f0] text-[#4f5d57]">
        <div className="border-b border-[#d7ddda] px-5 py-5">
          <div className="flex items-center gap-2">
            <span className="flex h-7 w-7 items-center justify-center rounded-[4px] border border-[#9fb9af] bg-white font-mono text-sm font-semibold text-[#23483d]">
              q
            </span>
            <span className="text-lg font-semibold text-[#121816]">quiver</span>
          </div>
          <div className="mt-2 text-[0.6875rem] font-semibold uppercase text-[#708078]">
            local skill operations
          </div>
        </div>

        {/* Global section — spans all projects. */}
        <div className="px-3 pt-4 text-[0.6875rem] font-semibold uppercase text-[#7a8580]">
          Global
        </div>
        <nav className="mt-1 space-y-1 px-3">
          <NavLink to="/registries" className={navClass}>
            Registries
          </NavLink>
        </nav>

        {/* Project switcher + scoped pages. */}
        <div className="mt-4 px-3 text-[0.6875rem] font-semibold uppercase text-[#7a8580]">
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

        <div className="border-t border-[#d7ddda] px-5 py-4 font-mono text-[0.6875rem] leading-relaxed text-[#708078]">
          read-only / local
          <br />
          qvr dashboard
        </div>
      </aside>
      <main className="flex-1 overflow-x-hidden">
        <div className="mx-auto max-w-6xl px-8 py-8">{children}</div>
      </main>
    </div>
  );
}
