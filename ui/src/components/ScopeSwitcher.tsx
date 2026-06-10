import { useEffect, useRef, useState } from "react";
import { ChevronsUpDown } from "lucide-react";
import { api, useFetch, type ProjectSummary, type Scope } from "../api";

// ScopeSwitcher is the project picker in the dark sidebar (.qvr-scope). It
// lists Quiver's known projects (from /api/projects) plus a Global entry, and
// rescopes every page when one is selected. The parent owns the active Scope
// and the remount token; this component only renders the menu and reports a
// new selection.
export default function ScopeSwitcher({
  scope,
  onChange,
}: {
  scope: Scope;
  onChange: (s: Scope) => void;
}) {
  const { data } = useFetch(api.projects, "projects");
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  // Close on outside click or Escape.
  useEffect(() => {
    function onDoc(e: MouseEvent) {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    }
    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape") setOpen(false);
    }
    document.addEventListener("mousedown", onDoc);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDoc);
      document.removeEventListener("keydown", onKey);
    };
  }, []);

  const projects = data ?? [];
  const label = activeLabel(scope, projects);

  function pick(s: Scope) {
    onChange(s);
    setOpen(false);
  }

  return (
    <div ref={ref} style={{ position: "relative" }}>
      <button
        type="button"
        className="qvr-scope"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
      >
        <span className="qvr-scope__name" style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
          {label}
        </span>
        <ChevronsUpDown />
      </button>
      {open && (
        <div className="qvr-scope-menu">
          {projects.map((p) => {
            const sel: Scope = p.scope === "global" ? { scope: "global" } : { project: p.path };
            return (
              <button
                key={p.scope === "global" ? "__global__" : p.path}
                type="button"
                className="qvr-scope-item"
                data-active={isActive(scope, p) ? "" : undefined}
                onClick={() => pick(sel)}
                title={p.path || "all projects"}
              >
                <div className="qvr-scope-item__name">
                  {p.name}
                  {p.current && p.scope === "project" ? " ·" : ""}
                </div>
                <div className="qvr-scope-item__sub">
                  {p.scope === "global" ? "all projects" : p.path}
                  {!p.hasLock && p.scope === "project" ? " · no lock" : ""}
                </div>
                <div className="qvr-scope-item__sub">
                  {p.skills} skills · {p.sessions} sessions
                </div>
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}

// isActive reports whether project row p matches the current scope. The default
// scope (empty) resolves to the launch project (p.current).
function isActive(scope: Scope, p: ProjectSummary): boolean {
  if (scope.scope) return p.scope === scope.scope;
  if (scope.project) return p.path === scope.project;
  return p.scope === "project" && p.current;
}

function activeLabel(scope: Scope, projects: ProjectSummary[]): string {
  if (scope.scope === "global") return "Global";
  if (scope.scope === "all") return "All projects";
  const match = projects.find((p) => isActive(scope, p));
  if (match) return match.name;
  return scope.project ? scope.project.split("/").pop() || scope.project : "This project";
}
