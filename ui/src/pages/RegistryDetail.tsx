import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import { api, useFetch, type RegistryVersion } from "../api";
import {
  Card,
  Empty,
  ErrorBox,
  Loading,
  Mono,
  PageHeader,
  Pill,
  short,
} from "../components/ui";

export default function RegistryDetail() {
  const { name = "" } = useParams();
  const { data, error, loading } = useFetch(
    () => api.registrySkills(name),
    `registry:${name}`,
  );

  return (
    <>
      <div className="mb-4">
        <Link to="/registries" className="text-sm font-medium text-[#2f765d] hover:underline">
          ← Registries
        </Link>
      </div>
      {loading && <Loading />}
      {error && <ErrorBox message={error} />}
      {data && (
        <>
          <PageHeader
            title={data.registry}
            subtitle={data.url || "Git repository Quiver installs skills from."}
          />
          {data.error && <ErrorBox message={data.error} />}

          {/* Skills the registry offers, with install status. Expanding a skill
              lists the versions (refs/tags) it can be installed at — no repo-wide
              version tree, just the skill and its versions. */}
          <Card title={`Skills (${data.skills.length})`}>
            {data.skills.length === 0 ? (
              <Empty>This registry has no indexable skills.</Empty>
            ) : (
              <ul className="divide-y divide-[#eef0ef]">
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
            {data.defaultBranch && (
              <p className="mt-3 text-xs text-[#708078]">
                Default branch: <Mono>{data.defaultBranch}</Mono>
              </p>
            )}
          </Card>
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
  skill: import("../api").RegistrySkillRow;
  versions: RegistryVersion[];
}) {
  const [open, setOpen] = useState(false);
  return (
    <li className="py-2">
      <div className="flex items-center gap-2">
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          aria-label={open ? "Collapse versions" : "Expand versions"}
          className="text-[#708078] hover:text-[#22302b]"
        >
          {open ? "▾" : "▸"}
        </button>
        <Link
          to={`/registries/${encodeURIComponent(registry)}/skills/${encodeURIComponent(
            skill.name,
          )}`}
          className="font-medium text-[#22302b] hover:text-[#2f765d] hover:underline"
        >
          {skill.name}
        </Link>
        {skill.installed ? (
          <Pill tone="green">
            installed{skill.installedRef ? ` @ ${skill.installedRef}` : ""}
          </Pill>
        ) : (
          <Pill tone="gray">available</Pill>
        )}
        {skill.installed && skill.installedCommit && (
          <Mono title={skill.installedCommit}>{short(skill.installedCommit)}</Mono>
        )}
      </div>
      {skill.description && (
        <p className="mt-0.5 pl-6 text-sm text-[#66736d]">{skill.description}</p>
      )}
      {open && (
        <div className="mt-2 pl-6">
          <div className="mb-1 text-xs font-semibold uppercase text-[#708078]">
            Versions {skill.installed ? "(installed one highlighted)" : ""}
          </div>
          {versions.length === 0 ? (
            <Empty>No branches or tags found in this registry's clone.</Empty>
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
    <ul className="flex flex-wrap gap-1.5">
      {versions.map((v) => {
        const isCurrent =
          (currentRef && v.ref === currentRef) ||
          (currentSha && v.sha === currentSha) ||
          (!currentRef && !currentSha && v.current);
        return (
          <li
            key={`${v.isTag ? "tag" : "branch"}:${v.ref}`}
            className={`flex items-center gap-1.5 rounded-[4px] px-2 py-1 text-sm ${
              isCurrent
                ? "bg-[#e8f4ef] ring-1 ring-inset ring-[#8cc8b0]"
                : "bg-[#f7f9f8]"
            }`}
          >
            <Pill tone={v.isTag ? "amber" : "blue"}>{v.isTag ? "tag" : "branch"}</Pill>
            <span
              className={`font-medium ${isCurrent ? "text-[#176548]" : "text-[#22302b]"}`}
            >
              {v.ref}
            </span>
            {isCurrent && <Pill tone="green">current</Pill>}
          </li>
        );
      })}
    </ul>
  );
}
