import { useMemo, useState } from "react";
import { Link, useParams } from "react-router-dom";
import {
  api,
  prettyAgent,
  scopeToken,
  useFetch,
  type Finding,
  type RegistryVersion,
  type RegistrySkillDetail,
  type ScanRunResult,
  type SkillInfo,
  type SkillReport,
} from "../api";
import FileTree, { fileKind } from "../components/FileTree";
import VersionTimeline, {
  fromRegistryVersions,
  fromVersionUsage,
} from "../components/VersionTimeline";
import {
  Back,
  Badge,
  BarRow,
  Button,
  Card,
  CopyChip,
  EmptyState,
  ErrorBox,
  Loading,
  Meta,
  MetaItem,
  DetailHeader,
  Section,
  ShareStat,
  Sparkline,
  StatCard,
  StatusBadge,
  Table,
  Tabs,
  Tag,
  Td,
  Th,
} from "../components/qvr";
import { fmtCount, fmtShare, fmtTime, relTime, short } from "../lib/format";
import { toneFor } from "../lib/tones";

// SkillView is the skill workbench, used from two routes:
//   /skills/:name                          — project scope (installed): the
//     REPORT CARD — observed behavior (utilization, verified share, cost,
//     lineage) in front, install plumbing behind a tab.
//   /registries/:registry/skills/:name     — registry scope (browsing): files
//     + version catalogue, no telemetry (nothing is installed).
// All loaders are declared unconditionally (returning null in the off mode) so
// hook order stays stable.

export default function SkillView({ mode }: { mode: "project" | "registry" }) {
  const params = useParams();
  const name = params.name ?? "";
  const registry = params.registry; // present only on the registry route

  // Installed skill (project mode).
  const proj = useFetch(
    () => (mode === "project" ? api.skill(name) : Promise.resolve(null)),
    `skillview:proj:${mode}:${name}:${scopeToken()}`,
  );
  // Observed behavior (project mode) — the report card data.
  const report = useFetch(
    () => (mode === "project" ? api.skillReport(name) : Promise.resolve(null)),
    `skillview:report:${mode}:${name}:${scopeToken()}`,
  );
  // Browsed skill (registry mode).
  const reg = useFetch(
    () =>
      mode === "registry" && registry
        ? api.registrySkill(registry, name)
        : Promise.resolve(null),
    `skillview:reg:${mode}:${registry ?? ""}:${name}`,
  );
  // The installed skill's available refs come from its registry's repo timeline.
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
      ? { to: `/registries/${encodeURIComponent(registry)}`, label: registry }
      : { to: "/skills", label: "Skills" };

  return (
    <>
      <Back to={back.to} label={back.label} />
      {loading && <Loading />}
      {error && <ErrorBox message={error} />}

      {mode === "project" && proj.data && (
        <ProjectView
          info={proj.data}
          report={report.data}
          versions={projVers.data?.versions ?? []}
        />
      )}
      {mode === "registry" && reg.data && <RegistryView detail={reg.data} />}
    </>
  );
}

// ---- project (installed): the report card ------------------------------------

function ProjectView({
  info,
  report,
  versions,
}: {
  info: SkillInfo;
  report: SkillReport | null;
  versions: RegistryVersion[];
}) {
  const [tab, setTab] = useState("report");

  return (
    <>
      <DetailHeader
        name={info.name}
        badges={
          <>
            <Badge tone="success" dot>
              installed{info.ref ? ` @ ${info.ref}` : ""}
            </Badge>
            {info.commitDrift && (
              <Badge tone="warning" dot title={`lock out of date: ${info.commitDrift}`}>
                drift
              </Badge>
            )}
          </>
        }
        desc={info.description}
      />

      <Meta>
        <MetaItem k="registry">{info.registry || "—"}</MetaItem>
        <MetaItem k="ref">{info.ref || "—"}</MetaItem>
        {info.commit && (
          <span className="qvr-meta__item">
            <span className="qvr-meta__k">commit</span>
            <Tag lead="#" title={info.commit}>
              {short(info.commit)}
            </Tag>
            <CopyChip value={info.commit} />
          </span>
        )}
        {info.mode && <MetaItem k="mode">{info.mode}</MetaItem>}
        {info.license && <MetaItem k="license">{info.license}</MetaItem>}
        {info.installedAt && <MetaItem k="installed">{fmtTime(info.installedAt)}</MetaItem>}
      </Meta>
      {info.source && (
        <Meta style={{ marginTop: 4 }}>
          <span className="qvr-meta__item">
            <span className="qvr-meta__k">source</span>
            <span className="qvr-meta__v" style={{ color: "var(--link)" }}>
              {info.source}
            </span>
            <CopyChip value={info.source} />
          </span>
        </Meta>
      )}

      <div style={{ marginTop: 18 }}>
        <Tabs
          items={[
            { id: "report", label: "report" },
            { id: "versions", label: "versions", count: report?.versions.length || undefined },
            { id: "install", label: "install", count: info.files?.length || undefined },
          ]}
          value={tab}
          onChange={setTab}
        />
      </div>

      {tab === "report" && <ReportTab report={report} />}
      {tab === "versions" && (
        <VersionsTab report={report} versions={versions} info={info} />
      )}
      {tab === "install" && <InstallTab info={info} />}
    </>
  );
}

// ReportTab — observed behavior: the verified-share hero, the utilization
// sparkline, token cost, per-agent table, recent sessions.
function ReportTab({ report }: { report: SkillReport | null }) {
  const sparkPoints = useMemo(() => {
    if (!report) return [];
    // Collapse per-agent day buckets into one total series, day-ordered.
    const byDay = new Map<string, number>();
    for (const p of report.series) {
      byDay.set(p.day, (byDay.get(p.day) ?? 0) + p.invocations);
    }
    return [...byDay.entries()].map(([label, value]) => ({ label, value }));
  }, [report]);

  if (!report) return <Loading />;
  if (!report.audit_enabled) {
    return (
      <EmptyState title="no telemetry">
        usage is unknown until the audit pipeline records sessions — qvr audit enable.
      </EmptyState>
    );
  }
  if (report.totals.invocations === 0) {
    return (
      <EmptyState title="never fired">
        installed but no agent has loaded this skill yet. it will show up here the first
        time one does.
      </EmptyState>
    );
  }

  const t = report.totals;
  const share = t.invocations > 0 ? t.verified / t.invocations : undefined;
  const maxTok = Math.max(report.tokens.input, report.tokens.output, 1);

  return (
    <>
      <div
        style={{ display: "grid", gridTemplateColumns: "repeat(3, 1fr)", gap: 12, marginTop: 16 }}
      >
        <ShareStat
          share={share}
          label={`${fmtCount(t.verified)} of ${fmtCount(t.invocations)} invocations lock-verified`}
          sub="load path proven to resolve into the locked worktree"
        />
        <StatCard
          icon={<span />}
          value={fmtCount(t.invocations)}
          label={`invocations · ${fmtCount(t.sessions)} sessions`}
        />
        <StatCard
          icon={<span />}
          value={fmtCount(report.tokens.input + report.tokens.output)}
          label="tokens in sessions where it fired"
        />
      </div>

      <Section title="utilization">
        <Card>
          <Sparkline points={sparkPoints} title="invocations per day" />
          <p className="qvr-sub" style={{ marginTop: 6 }}>
            {sparkPoints.length} active {sparkPoints.length === 1 ? "day" : "days"} · first
            fired {relTime(t.firstFired)} · last fired {relTime(t.lastFired)}
          </p>
        </Card>
      </Section>

      <Section title="cost">
        <Card>
          <BarRow
            label="input"
            value={report.tokens.input}
            max={maxTok}
            display={fmtCount(report.tokens.input)}
          />
          <div style={{ height: 8 }} />
          <BarRow
            label="output"
            value={report.tokens.output}
            max={maxTok}
            display={fmtCount(report.tokens.output)}
          />
          <p className="qvr-sub" style={{ marginTop: 10 }}>
            tokens summed over the LLM turns of sessions where this skill fired — a session
            that fired several skills counts toward each, so this is exposure, not
            exclusive cost.
          </p>
        </Card>
      </Section>

      <Section title="agents">
        <Table
          head={
            <tr>
              <Th>agent</Th>
              <Th>invocations</Th>
              <Th>verified</Th>
              <Th>sessions</Th>
              <Th>last fired</Th>
            </tr>
          }
        >
          {report.agents.map((a) => (
            <tr key={a.agent}>
              <Td>
                <Badge tone="info">{prettyAgent(a.agent)}</Badge>
              </Td>
              <Td muted>{fmtCount(a.invocations)}</Td>
              <Td muted>
                {fmtShare(a.invocations > 0 ? a.verified / a.invocations : undefined)}
              </Td>
              <Td muted>{fmtCount(a.sessions)}</Td>
              <Td muted>{relTime(a.lastFired)}</Td>
            </tr>
          ))}
        </Table>
      </Section>

      {report.recentSessions.length > 0 && (
        <Section title="recent sessions">
          <Card>
            <ul style={{ listStyle: "none", margin: 0, padding: 0 }}>
              {report.recentSessions.map((s) => (
                <li
                  key={s.session_id}
                  className="qvr-frow"
                  style={{ justifyContent: "space-between" }}
                >
                  <Link
                    to={`/sessions/${s.session_id}`}
                    style={{
                      minWidth: 0,
                      overflow: "hidden",
                      textOverflow: "ellipsis",
                      whiteSpace: "nowrap",
                    }}
                    title={s.title || s.session_id}
                  >
                    {s.title || "untitled session"}
                  </Link>
                  <span style={{ display: "flex", gap: 8, alignItems: "center", flex: "none" }}>
                    <Badge tone="info">{prettyAgent(s.agent_name)}</Badge>
                    <span className="qvr-ver__when">{relTime(s.started_at)}</span>
                  </span>
                </li>
              ))}
            </ul>
          </Card>
        </Section>
      )}
    </>
  );
}

// VersionsTab — lineage first (how each pinned version actually behaved),
// the registry's installable refs below.
function VersionsTab({
  report,
  versions,
  info,
}: {
  report: SkillReport | null;
  versions: RegistryVersion[];
  info: SkillInfo;
}) {
  return (
    <>
      <Section title="observed lineage">
        {report && report.versions.length > 0 ? (
          <>
            <VersionTimeline rows={fromVersionUsage(report.versions)} />
            <p className="qvr-sub" style={{ marginTop: 8 }}>
              one row per (ref, commit) this skill fired as. compare verified share and
              cost across versions before pinning forward.
            </p>
          </>
        ) : (
          <p className="qvr-sub">no observed runs yet — lineage appears once it fires.</p>
        )}
      </Section>
      <Section title="available refs">
        <VersionTimeline
          rows={fromRegistryVersions(versions, info.ref, info.commit)}
        />
      </Section>
    </>
  );
}

// InstallTab — the plumbing: files, targets, scan gate + live re-scan.
function InstallTab({ info }: { info: SkillInfo }) {
  const [scan, setScan] = useState<ScanRunResult | null>(null);
  const findingCounts = useMemo(() => countByFile(scan?.findings), [scan]);

  return (
    <div
      style={{
        display: "grid",
        gridTemplateColumns: "280px 1fr",
        gap: 16,
        marginTop: 16,
        alignItems: "start",
      }}
    >
      <div style={{ display: "grid", gap: 16 }}>
        <Card title={`files (${(info.files ?? []).length})`}>
          <div style={{ maxHeight: 448, overflowY: "auto" }}>
            <FileTree paths={info.files ?? []} findings={findingCounts} />
          </div>
        </Card>
        {info.targetDetails && info.targetDetails.length > 0 && (
          <Card title="targets">
            {info.targetDetails.map((t) => (
              <div key={t.target} className="qvr-frow">
                <span className="qvr-frow__name">{t.target}</span>
                <span className="qvr-frow__r">
                  <StatusBadge value={t.ok ? "success" : "error"} />
                </span>
              </div>
            ))}
          </Card>
        )}
      </div>
      <Card title="scan">
        <ProjectScanPanel info={info} scan={scan} onScan={setScan} />
      </Card>
    </div>
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
  const recorded = info.scan;

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
      ? `recorded gate "${recorded.decision}" but a live scan now reports "${scan.gate.decision}".`
      : null;

  return (
    <div style={{ display: "grid", gap: 14 }}>
      {/* Recorded install-time gate — instant, no scan needed. */}
      <div className="qvr-scan">
        <span className="qvr-scan__k">recorded gate</span>
        {recorded ? (
          <>
            <StatusBadge value={recorded.decision} />
            <SeverityChips
              counts={[
                ["critical", recorded.counts.critical],
                ["high", recorded.counts.high],
                ["medium", recorded.counts.medium],
                ["low", recorded.counts.low],
                ["info", recorded.counts.info],
              ]}
            />
            {recorded.scannerVersion && (
              <span className="qvr-scan__scanner">scanner {recorded.scannerVersion}</span>
            )}
          </>
        ) : (
          <span className="qvr-scan__scanner">not scanned at install</span>
        )}
        <span style={{ marginLeft: "auto" }}>
          <Button variant="primary" size="sm" onClick={run} disabled={busy}>
            {busy ? "scanning…" : scan ? "re-scan" : "run live scan"}
          </Button>
        </span>
      </div>

      {err && <ErrorBox message={err} />}
      {drift && (
        <div
          className="qvr-card"
          style={{
            borderColor: "var(--warning)",
            background: "var(--warning-soft)",
            padding: "10px 12px",
            fontFamily: "var(--font-mono)",
            fontSize: "var(--text-sm)",
            color: "var(--warning)",
          }}
        >
          {drift}
        </div>
      )}

      {scan && (
        <div style={{ borderTop: "1px solid var(--border-subtle)", paddingTop: 12, display: "grid", gap: 10 }}>
          <div className="qvr-scan">
            <span className="qvr-scan__k">live scan</span>
            <StatusBadge value={scan.gate.decision} />
            <span className="qvr-scan__scanner">threshold {scan.gate.threshold}</span>
            <SeverityChips
              counts={[
                ["critical", scan.summary.critical],
                ["error", scan.summary.error],
                ["warning", scan.summary.warning],
                ["info", scan.summary.info],
              ]}
            />
            {scan.lint && !scan.lint.valid && (
              <Badge
                tone="warning"
                title="agentskills.io spec lint — advisory, does not block install"
              >
                lint:{scan.lint.count}
              </Badge>
            )}
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

function RegistryView({ detail }: { detail: RegistrySkillDetail }) {
  return (
    <>
      <DetailHeader
        name={detail.name}
        badges={
          detail.installed ? (
            <Badge tone="success" dot>
              installed{detail.installedRef ? ` @ ${detail.installedRef}` : ""}
            </Badge>
          ) : (
            <Badge tone="info">browsing {detail.registry}</Badge>
          )
        }
        desc={detail.description}
      />
      <Meta>
        <MetaItem k="registry">{detail.registry}</MetaItem>
        {detail.ref && <MetaItem k="ref">{detail.ref}</MetaItem>}
        {detail.path && <MetaItem k="path">{detail.path}</MetaItem>}
        {detail.installed && detail.installedCommit && (
          <span className="qvr-meta__item">
            <span className="qvr-meta__k">installed commit</span>
            <Tag lead="#" title={detail.installedCommit}>
              {short(detail.installedCommit)}
            </Tag>
          </span>
        )}
      </Meta>
      {detail.error && (
        <div style={{ marginTop: 12 }}>
          <ErrorBox message={detail.error} />
        </div>
      )}

      <div
        style={{
          display: "grid",
          gridTemplateColumns: "280px 1fr",
          gap: 16,
          marginTop: 18,
          alignItems: "start",
        }}
      >
        <Card title={`files (${(detail.files ?? []).length})`}>
          <div style={{ maxHeight: 448, overflowY: "auto" }}>
            <FileTree paths={detail.files ?? []} />
          </div>
        </Card>
        <div style={{ display: "grid", gap: 16 }}>
          <Card title="scan">
            <div style={{ display: "grid", gap: 10 }}>
              <Inventory files={detail.files ?? []} />
              <p className="qvr-sub" style={{ margin: 0 }}>
                full security scan runs at install. install this skill to record a gate
                decision and enable a live re-scan.
              </p>
            </div>
          </Card>
          <Card title="versions">
            <VersionTimeline rows={fromRegistryVersions(detail.versions ?? [])} />
          </Card>
        </div>
      </div>
    </>
  );
}

// ---- small presentational helpers -------------------------------------------

// SeverityChips renders nonzero severity counts as toned badges; clean = green.
function SeverityChips({ counts }: { counts: [string, number][] }) {
  const nonzero = counts.filter(([, n]) => n > 0);
  if (nonzero.length === 0) {
    return (
      <Badge tone="success" dot>
        clean
      </Badge>
    );
  }
  return (
    <span style={{ display: "flex", flexWrap: "wrap", gap: 4 }}>
      {nonzero.map(([label, n]) => (
        <Badge key={label} tone={toneFor(label)}>
          {n} {label}
        </Badge>
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
    return <p className="qvr-sub">no findings — clean under the current checks.</p>;
  }
  const sorted = [...list].sort(
    (a, b) => SEV_ORDER.indexOf(a.severity) - SEV_ORDER.indexOf(b.severity),
  );
  return (
    <ul style={{ listStyle: "none", margin: 0, padding: 0 }}>
      {sorted.map((f, i) => (
        <li key={i} style={{ padding: "10px 0", borderTop: i > 0 ? "1px solid var(--border-subtle)" : "none" }}>
          <div style={{ display: "flex", flexWrap: "wrap", alignItems: "center", gap: 8 }}>
            <Badge tone={toneFor(f.severity)}>{f.severity}</Badge>
            {f.rule_id && (
              <Tag title={f.check}>{f.rule_id}</Tag>
            )}
            {f.file && (
              <Tag title={f.file}>
                {f.file}
                {f.line ? `:${f.line}` : ""}
              </Tag>
            )}
          </div>
          <p
            style={{
              margin: "6px 0 0",
              fontFamily: "var(--font-body)",
              fontSize: "var(--text-sm)",
              color: "var(--text)",
            }}
          >
            {f.message}
          </p>
          {f.evidence && (
            <pre
              style={{
                margin: "6px 0 0",
                overflowX: "auto",
                whiteSpace: "pre-wrap",
                wordBreak: "break-word",
                padding: "6px 8px",
                background: "var(--surface-inset)",
                border: "1px solid var(--border-subtle)",
                borderRadius: "var(--radius-sm)",
                fontFamily: "var(--font-code)",
                fontSize: "var(--text-xs)",
                color: "var(--text-muted)",
              }}
            >
              {f.evidence}
            </pre>
          )}
          {f.remediation && (
            <p className="qvr-sub" style={{ marginTop: 4 }}>
              ↳ {f.remediation}
            </p>
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
    <div className="qvr-scan">
      <span className="qvr-scan__k">inventory</span>
      {groups.map(([kind, n]) => (
        <span key={kind} className="qvr-pill">
          {n} {kind}
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
