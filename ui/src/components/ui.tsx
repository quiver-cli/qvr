import { useState, type ReactNode } from "react";

// Shared presentational primitives: status pills, stat cards, mono text, table
// shells, loading/error/empty states. Kept tiny and dependency-free.

type Tone = "green" | "red" | "amber" | "gray" | "blue";

const toneClasses: Record<Tone, string> = {
  green: "bg-[#e8f4ef] text-[#176548] ring-[#8cc8b0]",
  red: "bg-[#f8eaea] text-[#9a2f2f] ring-[#dc9a9a]",
  amber: "bg-[#f7efd9] text-[#77560f] ring-[#d3ba70]",
  gray: "bg-[#ecefed] text-[#47504c] ring-[#c8cecb]",
  blue: "bg-[#e7eff4] text-[#265c78] ring-[#9bb9c8]",
};

// toneFor maps the various status vocabularies (result_status, scan decision,
// signature status, enabled/disabled) onto a colour.
export function toneFor(value?: string): Tone {
  switch ((value ?? "").toLowerCase()) {
    case "success":
    case "allowed":
    case "verified":
    case "enabled":
    case "passed":
      return "green";
    case "critical":
    case "error":
    case "blocked":
    case "invalid":
    case "failed":
      return "red";
    case "warning":
      return "amber";
    case "skipped":
    case "none":
    case "unscanned":
    case "disabled":
    case "":
      return "gray";
    default:
      return "blue";
  }
}

export function Pill({
  children,
  tone,
  title,
}: {
  children: ReactNode;
  tone?: Tone;
  title?: string;
}) {
  return (
    <span
      title={title}
      className={`inline-flex items-center rounded-[3px] px-2 py-0.5 text-[0.6875rem] font-semibold uppercase leading-5 ring-1 ring-inset ${
        toneClasses[tone ?? "gray"]
      }`}
    >
      {children}
    </span>
  );
}

export function StatusPill({ value }: { value?: string }) {
  return <Pill tone={toneFor(value)}>{value || "—"}</Pill>;
}

export function StatCard({
  label,
  value,
  sub,
}: {
  label: string;
  value: ReactNode;
  sub?: ReactNode;
}) {
  return (
    <div className="rounded-[6px] border border-[#d7ddda] bg-white px-4 py-3 shadow-[0_1px_0_rgba(22,32,28,0.04)]">
      <div className="text-[0.6875rem] font-semibold uppercase text-[#6b746f]">{label}</div>
      <div className="mt-1 font-mono text-2xl font-semibold text-[#121816]">{value}</div>
      {sub != null && <div className="mt-0.5 text-xs text-[#68736e]">{sub}</div>}
    </div>
  );
}

export function Mono({ children, title }: { children: ReactNode; title?: string }) {
  return (
    <span title={title} className="font-mono text-[0.8125rem] text-[#34423d]">
      {children}
    </span>
  );
}

export function short(sha?: string, n = 7): string {
  if (!sha) return "—";
  const body = sha.startsWith("sha256:") ? sha.slice(7) : sha;
  return body.length > n ? body.slice(0, n) : body;
}

export function Card({ title, children }: { title?: string; children: ReactNode }) {
  return (
    <section className="rounded-[6px] border border-[#d7ddda] bg-white shadow-[0_1px_0_rgba(22,32,28,0.04)]">
      {title && (
        <div className="border-b border-[#e6e9e7] bg-[#fbfbfa] px-4 py-3 text-[0.8125rem] font-semibold uppercase text-[#4d5853]">
          {title}
        </div>
      )}
      <div className="p-4">{children}</div>
    </section>
  );
}

export function Table({
  head,
  children,
}: {
  head: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className="overflow-x-auto rounded-[6px] border border-[#d7ddda] bg-white shadow-[0_1px_0_rgba(22,32,28,0.04)]">
      <table className="min-w-full divide-y divide-[#e1e5e3] text-sm">
        <thead className="bg-[#f4f6f5] text-left text-[0.6875rem] font-semibold uppercase text-[#63706a]">
          {head}
        </thead>
        <tbody className="divide-y divide-[#eef0ef]">{children}</tbody>
      </table>
    </div>
  );
}

export function Th({ children }: { children: ReactNode }) {
  return <th className="px-4 py-2.5 font-semibold">{children}</th>;
}

export function Td({
  children,
  className,
  title,
}: {
  children: ReactNode;
  className?: string;
  title?: string;
}) {
  return (
    <td title={title} className={`px-4 py-2.5 align-top text-[#37413d] ${className ?? ""}`}>
      {children}
    </td>
  );
}

export function Loading() {
  return <div className="py-12 text-center text-sm text-[#7a8580]">Loading...</div>;
}

export function ErrorBox({ message }: { message: string }) {
  return (
    <div className="rounded-[6px] border border-[#dc9a9a] bg-[#f8eaea] px-4 py-3 text-sm text-[#842727]">
      {message}
    </div>
  );
}

export function Empty({ children }: { children: ReactNode }) {
  return <div className="py-12 text-center text-sm text-[#7a8580]">{children}</div>;
}

export function PageHeader({ title, subtitle }: { title: string; subtitle?: string }) {
  return (
    <header className="mb-6 border-b border-[#d7ddda] pb-4">
      <div className="text-[0.6875rem] font-semibold uppercase text-[#708078]">qvr ui</div>
      <h1 className="mt-1 text-2xl font-semibold text-[#121816]">{title}</h1>
      {subtitle && <p className="mt-1 max-w-3xl text-sm leading-6 text-[#66736d]">{subtitle}</p>}
    </header>
  );
}

// CopyButton copies value to the clipboard and flashes "copied" briefly. Used
// alongside raw-trace blocks so a session's JSON can be lifted out for an eval
// or skill-evolution pipeline without a round-trip through the DB.
export function CopyButton({ value }: { value: string }) {
  const [done, setDone] = useState(false);
  return (
    <button
      type="button"
      onClick={() => {
        void navigator.clipboard?.writeText(value).then(() => {
          setDone(true);
          setTimeout(() => setDone(false), 1200);
        });
      }}
      className="rounded-[4px] border border-[#cbd2ce] bg-white px-2 py-0.5 text-xs font-medium text-[#52615a] hover:bg-[#f4f6f5]"
    >
      {done ? "copied" : "copy"}
    </button>
  );
}

// CodeBlock renders preformatted text (typically pretty-printed JSON) in a
// scrollable mono panel, with an optional label row carrying a copy button.
export function CodeBlock({ value, label }: { value: string; label?: string }) {
  return (
    <div className="mt-2">
      {label && (
        <div className="mb-1 flex items-center justify-between">
          <span className="text-xs font-semibold uppercase text-[#708078]">{label}</span>
          <CopyButton value={value} />
        </div>
      )}
      <pre className="max-h-96 overflow-auto whitespace-pre rounded-[6px] border border-[#d7ddda] bg-[#f6f8f7] p-3 font-mono text-xs leading-relaxed text-[#34423d]">
        {value}
      </pre>
    </div>
  );
}

// prettyJSON stringifies an arbitrary JSON value with 2-space indent, falling
// back to String() on the rare circular/unstringifiable input.
export function prettyJSON(v: unknown): string {
  try {
    return JSON.stringify(v, null, 2);
  } catch {
    return String(v);
  }
}

export function fmtTime(iso?: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (isNaN(d.getTime())) return iso;
  return d.toLocaleString();
}
