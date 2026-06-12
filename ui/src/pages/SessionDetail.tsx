import { useMemo, useState } from "react";
import { useParams } from "react-router-dom";
import { ChevronDown, ChevronRight, Dot } from "lucide-react";
import { api, prettyAgent, useFetch, type RawTraceView, type SpanRow } from "../api";
import {
  Badge,
  CodeBlock,
  DetailHeader,
  EmptyState,
  ErrorBox,
  Loading,
  Back,
  Meta,
  MetaItem,
  Tabs,
  Tag,
  VersionTag,
} from "../components/qvr";
import { fmtEpochMs, fmtMs, fmtTime, prettyJSON, short } from "../lib/format";
import { spanKindTone } from "../lib/tones";

type View = "spans" | "raw";

export default function SessionDetail() {
  const { id = "" } = useParams();
  const { data, error, loading } = useFetch(() => api.session(id), `session:${id}`);
  const [view, setView] = useState<View>("spans");

  const session = data?.session;
  const title = session?.title || "untitled session";

  return (
    <>
      <Back to="/sessions" label="Sessions" />
      {loading && <Loading />}
      {error && <ErrorBox message={error} />}
      {data && session && (
        <>
          <DetailHeader
            name={title}
            badges={<Badge tone="info">{prettyAgent(session.agent_name)}</Badge>}
          />
          <Meta>
            <MetaItem k="started">{fmtEpochMs(session.started_ms)}</MetaItem>
            {session.ended_ms > session.started_ms && (
              <MetaItem k="duration">{fmtMs(session.ended_ms - session.started_ms)}</MetaItem>
            )}
            <MetaItem k="turns">{session.turns}</MetaItem>
            <MetaItem k="tools">{session.tools}</MetaItem>
            {session.model && <MetaItem k="model">{session.model}</MetaItem>}
            <span className="qvr-meta__item">
              <span className="qvr-meta__k">session</span>
              <Tag title={session.session_id}>{short(session.session_id, 8)}</Tag>
            </span>
          </Meta>
          {(session.working_directory || session.git_branch) && (
            <Meta style={{ marginTop: 4 }}>
              {session.working_directory && (
                <MetaItem k="cwd">{session.working_directory}</MetaItem>
              )}
              {session.git_branch && <MetaItem k="branch">{session.git_branch}</MetaItem>}
            </Meta>
          )}

          {/* Toggle between the processed (derived span) view and the lossless
              raw rows — the two representations of the same session. */}
          <div style={{ marginTop: 18 }}>
            <Tabs
              items={[
                { id: "spans", label: "spans", count: data.spans.length },
                { id: "raw", label: "raw", count: data.traces.length },
              ]}
              value={view}
              onChange={(v) => setView(v as View)}
            />
          </div>

          <div style={{ marginTop: 16 }}>
            {view === "spans" ? (
              <SpansView spans={data.spans} />
            ) : (
              <RawView traces={data.traces} />
            )}
          </div>
        </>
      )}
    </>
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
    error: str("error.type"),
  };
}

// Identity fields exist on a span only when its load path proved which locked
// artifact ran (#146, #149); the VersionTag renders them as the quiet pin —
// or @unknown when the agent's records carried no evidence. The skill NAME
// tag is the loud part: tagging the session is the point, identity is
// supporting metadata.

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
      <EmptyState title="no processed spans" art={false}>
        spans are derived from the transcript — switch to raw to see the captured bytes,
        or run qvr audit rederive.
      </EmptyState>
    );
  }
  return (
    <div style={{ display: "grid", gap: 14 }}>
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
    <div className="qvr-card">
      <div className="qvr-card__header" style={{ flexWrap: "wrap" }}>
        <span
          style={{ fontFamily: "var(--font-mono)", fontSize: "var(--text-xs)", color: "var(--text-faint)" }}
        >
          #{index}
        </span>
        <Badge tone={spanKindTone("LLM")}>LLM</Badge>
        <span className="qvr-card__title">{a.model || "chat"}</span>
        {(a.inTokens != null || a.outTokens != null) && (
          <span className="qvr-scan__scanner">
            {a.inTokens ?? 0} in / {a.outTokens ?? 0} out tok
          </span>
        )}
        {dur > 0 && <span className="qvr-scan__scanner">{fmtMs(dur)}</span>}
      </div>
      <div className="qvr-card__body" style={{ display: "grid", gap: 12 }}>
        {a.prompt && <MessageBlock label="prompt" tone="user" text={a.prompt} />}
        {a.output && <MessageBlock label="response" tone="assistant" text={a.output} />}
        {turn.children.length > 0 && (
          <div style={{ display: "grid", gap: 8 }}>
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
  const bar = tone === "user" ? "var(--info)" : "var(--success)";
  return (
    <div style={{ borderLeft: `2px solid ${bar}`, paddingLeft: 12 }}>
      <div className="qvr-meta__k" style={{ marginBottom: 2 }}>
        {label}
      </div>
      <div
        style={{
          whiteSpace: "pre-wrap",
          wordBreak: "break-word",
          fontFamily: "var(--font-body)",
          fontSize: "var(--text-sm)",
          lineHeight: "var(--leading-normal)",
          color: "var(--text)",
        }}
      >
        {text}
      </div>
    </div>
  );
}

function ToolSpan({ span }: { span: SpanRow }) {
  const [open, setOpen] = useState(false);
  const a = parseAttrs(span.attributes);
  const isSkill = span.kind === "SKILL";
  const hasDetail = !!(a.toolArgs || a.toolResult);
  return (
    <div className="qvr-card qvr-card--inset">
      <button
        type="button"
        onClick={() => hasDetail && setOpen((v) => !v)}
        style={{
          display: "flex",
          width: "100%",
          alignItems: "center",
          gap: 8,
          padding: "8px 12px",
          background: "none",
          border: "none",
          textAlign: "left",
          cursor: hasDetail ? "pointer" : "default",
          color: "var(--text-faint)",
        }}
      >
        {hasDetail ? (open ? <ChevronDown size={14} /> : <ChevronRight size={14} />) : <Dot size={14} />}
        <Badge tone={spanKindTone(span.kind)}>{span.kind}</Badge>
        <span
          style={{ fontFamily: "var(--font-mono)", fontSize: "var(--text-sm)", color: "var(--text)" }}
        >
          {a.toolName || span.name}
        </span>
        {isSkill && a.skillName && (
          <Badge tone="accent" dot>
            {a.skillName}
          </Badge>
        )}
        {isSkill && a.skillName && (
          <VersionTag
            refName={a.skillVersion}
            sha={a.skillCommit}
            title={
              a.skillRegistry
                ? `${a.skillRegistry}@${a.skillVersion}${a.skillCommit ? ` · ${a.skillCommit}` : ""}`
                : undefined
            }
          />
        )}
        {!isSkill && a.toolDesc && (
          <span
            className="qvr-scan__scanner"
            style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}
          >
            {a.toolDesc}
          </span>
        )}
        {a.error && <Badge tone="danger">{a.error}</Badge>}
      </button>
      {open && hasDetail && (
        <div style={{ padding: "0 12px 12px" }}>
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
    return (
      <EmptyState title="no raw rows" art={false}>
        nothing captured for this session.
      </EmptyState>
    );
  }
  return (
    <div style={{ display: "grid", gap: 8 }}>
      {traces.map((t) => (
        <RawRow key={t.seq} trace={t} />
      ))}
    </div>
  );
}

function RawRow({ trace }: { trace: RawTraceView }) {
  const [open, setOpen] = useState(false);
  return (
    <div className="qvr-card">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        style={{
          display: "flex",
          width: "100%",
          alignItems: "center",
          gap: 10,
          padding: "9px 14px",
          background: "none",
          border: "none",
          textAlign: "left",
          cursor: "pointer",
          color: "var(--text-faint)",
        }}
      >
        {open ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
        <span
          style={{
            width: 40,
            flex: "none",
            fontFamily: "var(--font-mono)",
            fontSize: "var(--text-xs)",
            color: "var(--text-faint)",
          }}
        >
          #{trace.seq}
        </span>
        <Badge tone={trace.source === "transcript" ? "info" : "warning"}>
          {trace.source === "hook_payload" ? "hook" : "transcript"}
        </Badge>
        {trace.hook_type && <Tag>{trace.hook_type}</Tag>}
        <span className="qvr-scan__scanner" style={{ marginLeft: "auto" }}>
          {fmtTime(trace.captured_at)}
        </span>
      </button>
      {open && (
        <div style={{ padding: "0 14px 12px" }}>
          <CodeBlock value={prettyJSON(trace.raw)} label="raw" />
          {trace.source_path && (
            <p className="qvr-scan__scanner" style={{ marginTop: 4 }}>
              {trace.source_path}
            </p>
          )}
        </div>
      )}
    </div>
  );
}
