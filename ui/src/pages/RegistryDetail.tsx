import { useState } from "react";
import { ChevronDown, ChevronRight } from "lucide-react";
import { Link, useParams } from "react-router-dom";
import { api, useFetch, type RegistrySkillRow, type RegistryVersion } from "../api";
import {
  Back,
  Badge,
  Card,
  DetailHeader,
  EmptyState,
  ErrorBox,
  Loading,
  Meta,
  MetaItem,
  Tag,
} from "../components/qvr";
import { short } from "../lib/format";

export default function RegistryDetail() {
  const { name = "" } = useParams();
  const { data, error, loading } = useFetch(
    () => api.registrySkills(name),
    `registry:${name}`,
  );

  return (
    <>
      <Back to="/registries" label="Registries" />
      {loading && <Loading />}
      {error && <ErrorBox message={error} />}
      {data && (
        <>
          <DetailHeader
            name={data.registry}
            desc={data.url || "Git repository Quiver installs skills from."}
          />
          {data.defaultBranch && (
            <Meta>
              <MetaItem k="default branch">{data.defaultBranch}</MetaItem>
              <MetaItem k="skills">{data.skills.length}</MetaItem>
            </Meta>
          )}
          {data.error && (
            <div style={{ marginTop: 12 }}>
              <ErrorBox message={data.error} />
            </div>
          )}

          {/* Skills the registry offers, with install status. Expanding a skill
              lists the versions (refs/tags) it can be installed at — no repo-wide
              version tree, just the skill and its versions. */}
          <div className="qvr-section">
            <Card title={`skills (${data.skills.length})`}>
              {data.skills.length === 0 ? (
                <EmptyState title="no indexable skills" art={false}>
                  this registry offers nothing qvr can index.
                </EmptyState>
              ) : (
                <ul style={{ listStyle: "none", margin: 0, padding: 0 }}>
                  {data.skills.map((s) => (
                    <SkillItem
                      key={s.name}
                      registry={name}
                      skill={s}
                      versions={data.versions}
                    />
                  ))}
                </ul>
              )}
            </Card>
          </div>
        </>
      )}
    </>
  );
}

function SkillItem({
  registry,
  skill,
  versions,
}: {
  registry: string;
  skill: RegistrySkillRow;
  versions: RegistryVersion[];
}) {
  const [open, setOpen] = useState(false);
  return (
    <li style={{ padding: "8px 0", borderTop: "1px solid var(--border-subtle)" }}>
      <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          aria-expanded={open}
          aria-label={open ? "collapse versions" : "expand versions"}
          style={{
            background: "none",
            border: "none",
            padding: 0,
            cursor: "pointer",
            color: "var(--text-faint)",
            display: "inline-flex",
          }}
        >
          {open ? <ChevronDown size={15} /> : <ChevronRight size={15} />}
        </button>
        <Link
          to={`/registries/${encodeURIComponent(registry)}/skills/${encodeURIComponent(
            skill.name,
          )}`}
          className="qvr-skillrow__name"
        >
          {skill.name}
        </Link>
        {skill.installed ? (
          <Badge tone="success" dot>
            installed{skill.installedRef ? ` @ ${skill.installedRef}` : ""}
          </Badge>
        ) : (
          <Badge tone="neutral">available</Badge>
        )}
        {skill.installed && skill.installedCommit && (
          <Tag lead="#" title={skill.installedCommit}>
            {short(skill.installedCommit)}
          </Tag>
        )}
      </div>
      {skill.description && (
        <p className="qvr-sub" style={{ margin: "2px 0 0 24px" }}>
          {skill.description}
        </p>
      )}
      {open && (
        <div style={{ margin: "8px 0 0 24px" }}>
          <div className="qvr-meta__k" style={{ marginBottom: 4 }}>
            versions{skill.installed ? " (installed one highlighted)" : ""}
          </div>
          {versions.length === 0 ? (
            <p className="qvr-sub">no branches or tags found in this registry's clone.</p>
          ) : (
            <SkillVersions
              versions={versions}
              currentRef={skill.installedRef}
              currentSha={skill.installedCommit}
            />
          )}
        </div>
      )}
    </li>
  );
}

// SkillVersions lists the refs (branches/tags) a skill can be installed at as a
// flat, compact set of chips — the installed/pinned one is highlighted. This is
// deliberately not a timeline: users browsing a skill just want to see which
// versions exist, not every commit's sha/date/subject.
function SkillVersions({
  versions,
  currentRef,
  currentSha,
}: {
  versions: RegistryVersion[];
  currentRef?: string;
  currentSha?: string;
}) {
  return (
    <ul
      style={{
        listStyle: "none",
        margin: 0,
        padding: 0,
        display: "flex",
        flexWrap: "wrap",
        gap: 6,
      }}
    >
      {versions.map((v) => {
        const isCurrent =
          (currentRef && v.ref === currentRef) ||
          (currentSha && v.sha === currentSha) ||
          (!currentRef && !currentSha && v.current);
        return (
          <li
            key={`${v.isTag ? "tag" : "branch"}:${v.ref}`}
            style={{
              display: "flex",
              alignItems: "center",
              gap: 6,
              padding: "4px 8px",
              borderRadius: "var(--radius-sm)",
              background: isCurrent ? "var(--success-soft)" : "var(--surface-inset)",
              border: `1px solid ${isCurrent ? "var(--success)" : "var(--border-subtle)"}`,
            }}
          >
            <span className="qvr-pill">{v.isTag ? "tag" : "branch"}</span>
            <span
              style={{
                fontFamily: "var(--font-mono)",
                fontSize: "var(--text-sm)",
                color: isCurrent ? "var(--success)" : "var(--text)",
              }}
            >
              {v.ref}
            </span>
            {isCurrent && (
              <Badge tone="success" dot>
                current
              </Badge>
            )}
          </li>
        );
      })}
    </ul>
  );
}
