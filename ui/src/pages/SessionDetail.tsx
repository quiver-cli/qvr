import { useMemo, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { api, prettyAgent, useFetch, type RawTraceView, type SpanRow } from "../api";
import {
  Card,
  CodeBlock,
  Empty,
  ErrorBox,
  fmtTime,
  Loading,
  Mono,
  PageHeader,
  Pill,
  prettyJSON,
  StatCard,
} from "../components/ui";

type View = "spans" | "raw";

export default function SessionDetail() {
  const { id = "" } = useParams();
  const { data, error, loading } = useFetch(() => api.session(id), `session:${id}`);
  const [view, setView] = useState<View>("spans");

  const session = data?.session;
  const title = session?.title || "untitled session";

  return (
    <>
      <div className="mb-4">
        <Link to="/sessions" className="text-sm font-medium text-[#2f765d] hover:underline">
          ← Sessions
        </Link>
      </div>
      {loading && <Loading />}
      {error && <ErrorBox message={error} />}
      {data && session && (
        <>
          <PageHeader
            title={title}
            subtitle={`${prettyAgent(session.agent_name)} · started ${fmtTime(
              session.started_at,
            )}`}
          />

          <div className="grid grid-cols-2 gap-4 md:grid-cols-4">
            <StatCard label="Harness" value={prettyAgent(session.agent_name)} />
            <StatCard label="Transcript lines" value={session.transcript_lines} />
            <StatCard label="Hook payloads" value={session.hook_payloads} />
            <StatCard label="Total rows" value={session.total_rows} />
          </div>

          {session.working_directory && (
            <div className="mt-4">
              <Card title="Working directory">
                <Mono>{session.working_directory}</Mono>
              </Card>
            </div>
          )}

          {/* Toggle between the processed (derived span) view and the lossless
              raw rows — the two representations of the same session. */}
          <div className="mt-8 mb-4 flex items-center justify-between">
            <h2 className="text-sm font-semibold uppercase text-[#4d5853]">
              {view === "spans"
                ? `Processed spans (${data.spans.length})`
                : `Raw traces (${data.traces.length})`}
            </h2>
            <Toggle view={view} onChange={setView} />
          </div>

          {view === "spans" ? (
            <SpansView spans={data.spans} />
          ) : (
            <RawView traces={data.traces} />
          )}
        </>
      )}
    </>
  );
}

function Toggle({ view, onChange }: { view: View; onChange: (v: View) => void }) {
  const opt = (v: View, label: string) => (
    <button
      type="button"
      onClick={() => onChange(v)}
      className={`rounded-md px-3 py-1 text-xs font-medium transition ${
        view === v
          ? "bg-white text-[#121816] shadow-[0_1px_0_rgba(22,32,28,0.04)]"
          : "text-[#63706a] hover:text-[#22302b]"
      }`}
    >
      {label}
    </button>
  );
  return (
    <div className="inline-flex rounded-[6px] border border-[#cbd2ce] bg-[#ecefed] p-0.5">
      {opt("spans", "Processed spans")}
      {opt("raw", "Raw traces")}
    </div>
  );
}

// ---- processed spans -------------------------------------------------------

interface ParsedAttrs {
  model?: string;
  inTokens?: number;
  outTokens?: number;
  prompt?: string;
  output?: string;
  toolName?: string;
  toolArgs?: string;
  toolResult?: string;
  toolDesc?: string;
  skillName?: string;
  skillRegistry?: string;
  skillVersion?: string;
  skillCommit?: string;
  skillVerified?: boolean;
  error?: string;
}

function parseAttrs(raw: string): ParsedAttrs {
  let a: Record<string, unknown> = {};
  try {
    a = JSON.parse(raw || "{}") as Record<string, unknown>;
  } catch {
    return {};
  }
  const str = (k: string) => (typeof a[k] === "string" ? (a[k] as string) : undefined);
  const num = (k: string) => (typeof a[k] === "number" ? (a[k] as number) : undefined);
  const bool = (k: string) => (typeof a[k] === "boolean" ? (a[k] as boolean) : undefined);
  const firstMessage = (k: string): string | undefined => {
    const v = str(k);
    if (!v) return undefined;
    try {
      const msgs = JSON.parse(v) as { content?: string }[];
      return msgs?.map((m) => m.content).filter(Boolean).join("\n") || undefined;
    } catch {
      return v;
    }
  };
  return {
    model: str("gen_ai.request.model"),
    inTokens: num("gen_ai.usage.input_tokens"),
    outTokens: num("gen_ai.usage.output_tokens"),
    prompt: firstMessage("gen_ai.input.messages"),
    output: firstMessage("gen_ai.output.messages"),
    toolName: str("gen_ai.tool.name"),
    toolArgs: str("gen_ai.tool.call.arguments"),
    toolResult: str("gen_ai.tool.call.result"),
    toolDesc: str("gen_ai.tool.description"),
    skillName: str("skill.name"),
    skillRegistry: str("skill.registry"),
    skillVersion: str("skill.version"),
    skillCommit: str("skill.commit"),
    skillVerified: bool("skill.verified"),
    error: str("error.type"),
  };
}

// skillIdentity renders the lock-resolved skill identity as a compact label,
// e.g. "raks@v0.2.0 · 94e539b" — what makes name collisions across registries
// and versions distinguishable (#146). Returns "" when nothing was resolved.
function skillIdentity(a: ReturnType<typeof parseAttrs>): string {
  if (!a.skillRegistry && !a.skillVersion && !a.skillCommit) return "";
  const left = [a.skillRegistry, a.skillVersion].filter(Boolean).join("@");
  const sha = a.skillCommit ? a.skillCommit.slice(0, 7) : "";
  return [left, sha].filter(Boolean).join(" · ");
}

interface Turn {
  llm: SpanRow;
  children: SpanRow[];
}

// groupTurns assembles the turn hierarchy: each LLM span is a turn root, and
// every TOOL/SKILL span is parented to its turn's LLM span. Spans with no LLM
// parent (rare: a resumed session) get a synthetic turn so nothing is dropped.
function groupTurns(spans: SpanRow[]): Turn[] {
  const llmById = new Map<string, Turn>();
  const turns: Turn[] = [];
  for (const sp of spans) {
    if (sp.kind === "LLM") {
      const t: Turn = { llm: sp, children: [] };
      llmById.set(sp.span_id, t);
      turns.push(t);
    }
  }
  const orphans: SpanRow[] = [];
  for (const sp of spans) {
    if (sp.kind === "LLM") continue;
    const parent = sp.parent_span_id ? llmById.get(sp.parent_span_id) : undefined;
    if (parent) parent.children.push(sp);
    else orphans.push(sp);
  }
  if (orphans.length > 0) {
    // First orphan heads the synthetic turn; the rest are its children. Passing
    // the full orphans array as children too would render orphans[0] twice.
    turns.push({ llm: orphans[0], children: orphans.slice(1) });
  }
  for (const t of turns) t.children.sort((a, b) => a.start_ms - b.start_ms);
  turns.sort((a, b) => a.llm.start_ms - b.llm.start_ms);
  return turns;
}

function SpansView({ spans }: { spans: SpanRow[] }) {
  const turns = useMemo(() => groupTurns(spans), [spans]);
  if (spans.length === 0) {
    return (
      <Empty>
        No processed spans for this session. Spans are derived from the transcript —
        switch to <strong>Raw traces</strong> to see the captured bytes, or run{" "}
        <code className="rounded-[3px] bg-[#ecefed] px-1.5 py-0.5">qvr audit derive</code>.
      </Empty>
    );
  }
  return (
    <div className="space-y-4">
      {turns.map((t, i) => (
        <TurnCard key={t.llm.span_id} turn={t} index={i + 1} />
      ))}
    </div>
  );
}

function TurnCard({ turn, index }: { turn: Turn; index: number }) {
  const a = parseAttrs(turn.llm.attributes);
  const dur = turn.llm.end_ms - turn.llm.start_ms;
  return (
    <div className="rounded-[6px] border border-[#d7ddda] bg-white shadow-[0_1px_0_rgba(22,32,28,0.04)]">
      <div className="flex flex-wrap items-center gap-2 border-b border-[#e6e9e7] bg-[#fbfbfa] px-4 py-3">
        <span className="font-mono text-xs font-semibold text-[#708078]">#{index}</span>
        <Pill tone="blue">LLM</Pill>
        <span className="font-medium text-[#22302b]">{a.model || "chat"}</span>
        {(a.inTokens != null || a.outTokens != null) && (
          <span className="font-mono text-xs text-[#708078]">
            {a.inTokens ?? 0} in / {a.outTokens ?? 0} out tokens
          </span>
        )}
        {dur > 0 && <span className="font-mono text-xs text-[#708078]">{(dur / 1000).toFixed(1)}s</span>}
      </div>
      <div className="space-y-3 p-4">
        {a.prompt && <MessageBlock label="Prompt" tone="user" text={a.prompt} />}
        {a.output && <MessageBlock label="Response" tone="assistant" text={a.output} />}
        {turn.children.length > 0 && (
          <div className="space-y-2 pt-1">
            {turn.children.map((c) => (
              <ToolSpan key={c.span_id} span={c} />
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

function MessageBlock({
  label,
  tone,
  text,
}: {
  label: string;
  tone: "user" | "assistant";
  text: string;
}) {
  const bar = tone === "user" ? "border-[#9bb9c8]" : "border-[#8cc8b0]";
  return (
    <div className={`border-l-2 ${bar} pl-3`}>
      <div className="mb-0.5 text-xs font-semibold uppercase text-[#708078]">
        {label}
      </div>
      <div className="whitespace-pre-wrap break-words text-sm leading-6 text-[#34423d]">{text}</div>
    </div>
  );
}

function ToolSpan({ span }: { span: SpanRow }) {
  const [open, setOpen] = useState(false);
  const a = parseAttrs(span.attributes);
  const isSkill = span.kind === "SKILL";
  const hasDetail = !!(a.toolArgs || a.toolResult);
  return (
    <div className="rounded-[6px] border border-[#e1e5e3] bg-[#f7f9f8]">
      <button
        type="button"
        onClick={() => hasDetail && setOpen((v) => !v)}
        className={`flex w-full items-center gap-2 px-3 py-2 text-left ${
          hasDetail ? "cursor-pointer hover:bg-[#eef2f0]" : "cursor-default"
        }`}
      >
        <span className="text-[#708078]">{hasDetail ? (open ? "▾" : "▸") : "•"}</span>
        <Pill tone={isSkill ? "amber" : "gray"}>{span.kind}</Pill>
        <span className="font-medium text-[#22302b]">{a.toolName || span.name}</span>
        {isSkill && a.skillName && (
          <span className="text-xs text-[#63706a]">→ {a.skillName}</span>
        )}
        {isSkill && skillIdentity(a) && (
          <span
            className={
              a.skillVerified === false
                ? "rounded-[3px] bg-[#ecefed] px-1.5 py-0.5 font-mono text-[11px] text-[#63706a]"
                : "rounded-[3px] bg-[#f7efd9] px-1.5 py-0.5 font-mono text-[11px] text-[#77560f]"
            }
            title={
              a.skillVerified === false
                ? "best-guess identity from qvr.lock — qvr could not confirm the copy the agent actually loaded"
                : "skill identity verified against the loaded artifact"
            }
          >
            {a.skillVerified === false ? "~" : ""}
            {skillIdentity(a)}
          </span>
        )}
        {isSkill && !skillIdentity(a) && a.skillVerified === false && (
          <span
            className="rounded-[3px] bg-[#ecefed] px-1.5 py-0.5 text-[11px] text-[#63706a]"
            title="qvr could not resolve the loaded copy to a locked skill (e.g. a global eject or a shadowing install)"
          >
            unverified
          </span>
        )}
        {!isSkill && a.toolDesc && (
          <span className="truncate text-xs text-[#708078]">{a.toolDesc}</span>
        )}
        {a.error && <Pill tone="red">{a.error}</Pill>}
      </button>
      {open && hasDetail && (
        <div className="px-3 pb-3">
          {a.toolArgs && <CodeBlock value={pretty(a.toolArgs)} label="arguments" />}
          {a.toolResult && <CodeBlock value={a.toolResult} label="result" />}
        </div>
      )}
    </div>
  );
}

// pretty re-indents a JSON string when it parses, else returns it unchanged.
function pretty(s: string): string {
  try {
    return JSON.stringify(JSON.parse(s), null, 2);
  } catch {
    return s;
  }
}

// ---- raw traces ------------------------------------------------------------

function RawView({ traces }: { traces: RawTraceView[] }) {
  if (traces.length === 0) {
    return <Empty>No raw rows captured for this session.</Empty>;
  }
  return (
    <div className="space-y-2">
      {traces.map((t) => (
        <RawRow key={t.seq} trace={t} />
      ))}
    </div>
  );
}

function RawRow({ trace }: { trace: RawTraceView }) {
  const [open, setOpen] = useState(false);
  return (
    <div className="rounded-[6px] border border-[#d7ddda] bg-white shadow-[0_1px_0_rgba(22,32,28,0.04)]">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="flex w-full items-center gap-3 px-4 py-2.5 text-left hover:bg-[#f7f9f8]"
      >
        <span className="text-[#708078]">{open ? "▾" : "▸"}</span>
        <span className="w-10 shrink-0 font-mono text-xs text-[#708078]">#{trace.seq}</span>
        <Pill tone={trace.source === "transcript" ? "blue" : "amber"}>
          {trace.source === "hook_payload" ? "hook" : "transcript"}
        </Pill>
        {trace.hook_type && <Mono>{trace.hook_type}</Mono>}
        <span className="ml-auto text-xs text-[#708078]">{fmtTime(trace.captured_at)}</span>
      </button>
      {open && (
        <div className="px-4 pb-3">
          <CodeBlock value={prettyJSON(trace.raw)} label="raw" />
          {trace.source_path && (
            <div className="mt-1 text-xs text-[#708078]">
              <Mono>{trace.source_path}</Mono>
            </div>
          )}
        </div>
      )}
    </div>
  );
}
