import { useMemo, useState, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import {
  api,
  useFetch,
  type Finding,
  type ScanRunResult,
  type SkillInfo,
} from "../api";
import FileTree, { fileKind } from "../components/FileTree";
import VersionRailway from "../components/VersionRailway";
import {
  Card,
  CopyButton,
  Empty,
  ErrorBox,
  fmtTime,
  Loading,
  Mono,
  Pill,
  short,
  StatusPill,
} from "../components/ui";

// SkillView is the unified skill workbench, used from two routes:
//   /skills/:name                          — project scope (installed)
//   /registries/:registry/skills/:name     — registry scope (browsing)
// Both render the same three-part view — an identity header, a file tree, and a
// version railway — and differ only in their data source and scan panel. The
// installed view shows the recorded scan gate plus an on-demand live re-scan;
// the registry view, where nothing is checked out, shows a file-type inventory
// and points at install-time scanning. All three primary loaders are declared
// unconditionally (returning null in the off mode) so hook order stays stable.

export default function SkillView({ mode }: { mode: "project" | "registry" }) {
  const params = useParams();
  const name = params.name ?? "";
  const registry = params.registry; // present only on the registry route

  // Installed skill (project mode).
  const proj = useFetch(
    () => (mode === "project" ? api.skill(name) : Promise.resolve(null)),
    `skillview:proj:${mode}:${name}`,
  );
  // Browsed skill (registry mode).
  const reg = useFetch(
    () =>
      mode === "registry" && registry
        ? api.registrySkill(registry, name)
        : Promise.resolve(null),
    `skillview:reg:${mode}:${registry ?? ""}:${name}`,
  );
  // The installed skill's versions come from its registry's repo timeline.
  const projRegistry = proj.data?.registry;
  const projVers = useFetch(
    () =>
      mode === "project" && projRegistry
        ? api.registrySkills(projRegistry)
        : Promise.resolve(null),
    `skillview:projver:${mode}:${projRegistry ?? ""}`,
  );

  const loading = mode === "project" ? proj.loading : reg.loading;
  const error = mode === "project" ? proj.error : reg.error;

  const back =
    mode === "registry" && registry
      ? { to: `/registries/${encodeURIComponent(registry)}`, label: `← ${registry}` }
      : { to: "/skills", label: "← Skills" };

  return (
    <>
      <div className="mb-4">
        <Link to={back.to} className="text-sm font-medium text-[#2f765d] hover:underline">
          {back.label}
        </Link>
      </div>
      {loading && <Loading />}
      {error && <ErrorBox message={error} />}

      {mode === "project" && proj.data && (
        <ProjectView info={proj.data} versions={projVers.data?.versions ?? []} />
      )}
      {mode === "registry" && reg.data && <RegistryView detail={reg.data} />}
    </>
  );
}

// ---- project (installed) ----------------------------------------------------

function ProjectView({
  info,
  versions,
}: {
  info: SkillInfo;
  versions: import("../api").RegistryVersion[];
}) {
  const [scan, setScan] = useState<ScanRunResult | null>(null);
  const findingCounts = useMemo(() => countByFile(scan?.findings), [scan]);

  return (
    <>
      <Header
        name={info.name}
        description={info.description}
        badge={
          <Pill tone="green">
            installed{info.ref ? ` @ ${info.ref}` : ""}
          </Pill>
        }
        chips={
          <>
            <ChipRow label="registry" value={info.registry} />
            <ChipRow label="ref" value={info.ref} />
            <ChipRow label="commit" value={info.commit} mono copy short />
            {info.commitDrift && (
              <span className="text-xs text-red-600">
                drift: <Mono>{short(info.commitDrift)}</Mono> (lock out of date)
              </span>
            )}
            <ChipRow label="source" value={info.source} mono copy />
            <ChipRow label="license" value={info.license} />
            <ChipRow
              label="mode"
              value={info.mode || "shared"}
              tone={info.mode ? "amber" : undefined}
            />
            <ChipRow
              label="installed"
              value={info.installedAt ? fmtTime(info.installedAt) : undefined}
            />
          </>
        }
      />

      <Workbench
        files={info.files ?? []}
        findingCounts={findingCounts}
        versions={versions}
        selectedRef={info.ref}
        selectedSha={info.commit}
        scanPanel={<ProjectScanPanel info={info} scan={scan} onScan={setScan} />}
        targets={info.targetDetails}
      />
    </>
  );
}

function ProjectScanPanel({
  info,
  scan,
  onScan,
}: {
  info: SkillInfo;
  scan: ScanRunResult | null;
  onScan: (r: ScanRunResult) => void;
}) {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const recorded = info.verification?.scan;

  async function run() {
    setBusy(true);
    setErr(null);
    try {
      onScan(await api.runScan(info.name));
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  const drift =
    scan && recorded && scan.gate.decision !== recorded.decision
      ? `Recorded gate "${recorded.decision}" but a live scan now reports "${scan.gate.decision}".`
      : null;

  return (
    <div className="space-y-4">
      {/* Recorded install-time gate — instant, no scan needed. */}
      <div className="flex flex-wrap items-center gap-2">
            <span className="text-xs font-semibold uppercase text-[#708078]">
              Recorded gate
            </span>
        {recorded ? (
          <>
            <StatusPill value={recorded.decision} />
            <SeverityCounts
              counts={[
                ["critical", recorded.counts.critical, "red"],
                ["high", recorded.counts.high, "red"],
                ["medium", recorded.counts.medium, "amber"],
                ["low", recorded.counts.low, "gray"],
                ["info", recorded.counts.info, "gray"],
              ]}
            />
            {recorded.scannerVersion && (
              <span className="text-xs text-[#708078]">
                scanner {recorded.scannerVersion}
              </span>
            )}
          </>
        ) : (
          <span className="text-sm text-[#708078]">not scanned at install</span>
        )}
        <button
          type="button"
          onClick={run}
          disabled={busy}
          className="ml-auto rounded-[4px] bg-[#123a2e] px-3 py-1.5 text-xs font-semibold text-white hover:bg-[#1d513f] disabled:opacity-50"
        >
          {busy ? "scanning…" : scan ? "re-scan" : "run live scan"}
        </button>
      </div>

      {err && <ErrorBox message={err} />}
      {drift && (
        <div className="rounded-[6px] border border-[#d3ba70] bg-[#f7efd9] px-3 py-2 text-sm text-[#6c5012]">
          {drift}
        </div>
      )}

      {scan && (
        <div className="space-y-3 border-t border-[#e6e9e7] pt-3">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-xs font-semibold uppercase text-[#708078]">
              Live scan
            </span>
            <StatusPill value={scan.gate.decision} />
            <span className="text-xs text-[#708078]">
              threshold {scan.gate.threshold}
            </span>
            <SeverityCounts
              counts={[
                ["critical", scan.summary.critical, "red"],
                ["error", scan.summary.error, "red"],
                ["warning", scan.summary.warning, "amber"],
                ["info", scan.summary.info, "gray"],
              ]}
            />
          </div>
          <FindingsList findings={scan.findings} />
        </div>
      )}

      {/* File-type inventory — always available, even before a scan runs. */}
      <Inventory files={info.files ?? []} />
    </div>
  );
}

// ---- registry (browsing) ----------------------------------------------------

function RegistryView({ detail }: { detail: import("../api").RegistrySkillDetail }) {
  return (
    <>
      <Header
        name={detail.name}
        description={detail.description}
        badge={
          detail.installed ? (
            <Pill tone="green">
              installed{detail.installedRef ? ` @ ${detail.installedRef}` : ""}
            </Pill>
          ) : (
            <Pill tone="blue">browsing {detail.registry}</Pill>
          )
        }
        chips={
          <>
            <ChipRow label="registry" value={detail.registry} />
            <ChipRow label="ref" value={detail.ref} />
            <ChipRow label="path" value={detail.path} mono />
            {detail.installed && (
              <ChipRow
                label="installed commit"
                value={detail.installedCommit}
                mono
                short
              />
            )}
          </>
        }
      />
      {detail.error && <ErrorBox message={detail.error} />}
      <Workbench
        files={detail.files ?? []}
        versions={detail.versions ?? []}
        // Registry browse: no version is "selected" — the rail is a catalogue.
        scanPanel={
          <div className="space-y-3">
            <Inventory files={detail.files ?? []} />
            <p className="text-xs text-[#708078]">
              Full security scan runs at install. Install this skill to record a
              gate decision and enable a live re-scan.
            </p>
          </div>
        }
      />
    </>
  );
}

// ---- shared layout ----------------------------------------------------------

function Header({
  name,
  description,
  badge,
  chips,
}: {
  name: string;
  description?: string;
  badge: ReactNode;
  chips: ReactNode;
}) {
  return (
    <div className="mb-6">
      <div className="flex flex-wrap items-center gap-3">
        <h1 className="text-2xl font-semibold text-[#121816]">{name}</h1>
        {badge}
      </div>
      {description && <p className="mt-1 max-w-3xl text-sm leading-6 text-[#66736d]">{description}</p>}
      <div className="mt-3 flex flex-wrap items-center gap-x-5 gap-y-1.5">{chips}</div>
    </div>
  );
}

function Workbench({
  files,
  findingCounts,
  versions,
  selectedRef,
  selectedSha,
  scanPanel,
  targets,
}: {
  files: string[];
  findingCounts?: Record<string, number>;
  versions: import("../api").RegistryVersion[];
  selectedRef?: string;
  selectedSha?: string;
  scanPanel: ReactNode;
  targets?: SkillInfo["targetDetails"];
}) {
  return (
    <div className="grid grid-cols-1 gap-6 lg:grid-cols-[280px_1fr]">
      <div className="space-y-6">
        <Card title={`Files (${files.length})`}>
          <div className="max-h-[28rem] overflow-y-auto">
            <FileTree paths={files} findings={findingCounts} />
          </div>
        </Card>
        {targets && targets.length > 0 && (
          <Card title="Targets">
            <ul className="space-y-2">
              {targets.map((t) => (
                <li
                  key={t.target}
                  className="flex items-center justify-between text-sm"
                >
                  <span className="font-medium text-[#34423d]">{t.target}</span>
                  <StatusPill value={t.ok ? "success" : "error"} />
                </li>
              ))}
            </ul>
          </Card>
        )}
      </div>

      <div className="space-y-6">
        <Card title="Scan">{scanPanel}</Card>
        <Card title="Versions">
          <VersionRailway
            versions={versions}
            selectedRef={selectedRef}
            selectedSha={selectedSha}
          />
        </Card>
      </div>
    </div>
  );
}

// ---- small presentational helpers -------------------------------------------

function ChipRow({
  label,
  value,
  mono,
  copy,
  short: doShort,
  tone,
}: {
  label: string;
  value?: string;
  mono?: boolean;
  copy?: boolean;
  short?: boolean;
  tone?: "amber";
}) {
  if (!value) return null;
  const shown = doShort ? short(value) : value;
  return (
    <span className="flex items-center gap-1.5 text-xs">
      <span className="font-semibold uppercase text-[#708078]">{label}</span>
      {tone === "amber" ? (
        <Pill tone="amber">{value}</Pill>
      ) : mono ? (
        <Mono title={value}>{shown}</Mono>
      ) : (
        <span className="text-[#34423d]">{shown}</span>
      )}
      {copy && <CopyButton value={value} />}
    </span>
  );
}

type CountTone = "red" | "amber" | "gray";

function SeverityCounts({ counts }: { counts: [string, number, CountTone][] }) {
  const nonzero = counts.filter(([, n]) => n > 0);
  if (nonzero.length === 0) {
    return <Pill tone="green">clean</Pill>;
  }
  const tones: Record<CountTone, string> = {
    red: "bg-[#f8eaea] text-[#9a2f2f] ring-[#dc9a9a]",
    amber: "bg-[#f7efd9] text-[#77560f] ring-[#d3ba70]",
    gray: "bg-[#ecefed] text-[#47504c] ring-[#c8cecb]",
  };
  return (
    <span className="flex flex-wrap items-center gap-1">
      {nonzero.map(([label, n, tone]) => (
        <span
          key={label}
          className={`inline-flex items-center gap-1 rounded-[3px] px-2 py-0.5 text-xs font-semibold ring-1 ring-inset ${tones[tone]}`}
        >
          <span className="font-semibold">{n}</span>
          {label}
        </span>
      ))}
    </span>
  );
}

const SEV_ORDER = ["critical", "error", "warning", "info"];

function FindingsList({ findings }: { findings: Finding[] | null }) {
  // The scanner emits a null `findings` (not []) when a skill is clean, so
  // normalise before touching it.
  const list = findings ?? [];
  if (list.length === 0) {
    return <Empty>No findings — the skill is clean under the current checks.</Empty>;
  }
  const sorted = [...list].sort(
    (a, b) => SEV_ORDER.indexOf(a.severity) - SEV_ORDER.indexOf(b.severity),
  );
  const tone = (s: string) =>
    s === "critical" || s === "error" ? "red" : s === "warning" ? "amber" : "gray";
  return (
    <ul className="divide-y divide-[#eef0ef]">
      {sorted.map((f, i) => (
        <li key={i} className="py-2.5">
          <div className="flex flex-wrap items-center gap-2">
            <Pill tone={tone(f.severity)}>{f.severity}</Pill>
            {f.rule_id && <Mono title={f.check}>{f.rule_id}</Mono>}
            {f.file && (
              <Mono title={f.file}>
                {f.file}
                {f.line ? `:${f.line}` : ""}
              </Mono>
            )}
          </div>
          <p className="mt-1 text-sm text-[#34423d]">{f.message}</p>
          {f.evidence && (
            <pre className="mt-1 overflow-x-auto whitespace-pre-wrap break-words rounded-[4px] bg-[#f6f8f7] px-2 py-1 font-mono text-xs text-[#52615a]">
              {f.evidence}
            </pre>
          )}
          {f.remediation && (
            <p className="mt-0.5 text-xs text-[#63706a]">↳ {f.remediation}</p>
          )}
        </li>
      ))}
    </ul>
  );
}

function Inventory({ files }: { files: string[] }) {
  const groups = useMemo(() => {
    const m = new Map<string, number>();
    for (const f of files) {
      const k = fileKind(f);
      m.set(k, (m.get(k) ?? 0) + 1);
    }
    return [...m.entries()].sort((a, b) => b[1] - a[1]);
  }, [files]);
  if (groups.length === 0) return null;
  return (
    <div className="flex flex-wrap items-center gap-1.5">
      <span className="text-xs font-semibold uppercase text-[#708078]">
        Inventory
      </span>
      {groups.map(([kind, n]) => (
        <span
          key={kind}
          className="inline-flex items-center gap-1 rounded-[3px] bg-[#f7f9f8] px-2 py-0.5 text-xs text-[#52615a] ring-1 ring-inset ring-[#d7ddda]"
        >
          <span className="font-mono font-semibold text-[#22302b]">{n}</span>
          {kind}
        </span>
      ))}
    </div>
  );
}

// countByFile tallies findings per file path so the file tree can badge exactly
// which files a live scan flagged.
function countByFile(findings?: Finding[] | null): Record<string, number> {
  const out: Record<string, number> = {};
  for (const f of findings ?? []) {
    if (f.file) out[f.file] = (out[f.file] ?? 0) + 1;
  }
  return out;
}
