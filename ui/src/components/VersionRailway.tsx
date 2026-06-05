import type { RegistryVersion } from "../api";
import { Mono, Pill, short } from "./ui";

// VersionRailway renders a registry repo's branch/tag timeline as a vertical
// "railway": dots threaded on a single rail, newest first. It is the skill
// view's git graph. In project scope the installed ref/commit is passed in and
// that node lights up emerald with a "current" marker; in registry-browse scope
// nothing is selected, so the rail is a neutral catalogue of installable
// versions. The repo's default branch is tagged separately ("default") so "the
// version I have" and "the repo's default" stay visually distinct.

function relTime(iso?: string): string {
  if (!iso) return "";
  const t = new Date(iso).getTime();
  if (isNaN(t)) return "";
  const secs = Math.round((Date.now() - t) / 1000);
  if (secs < 60) return "just now";
  const mins = Math.round(secs / 60);
  if (mins < 60) return `${mins}m ago`;
  const hours = Math.round(mins / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.round(hours / 24);
  if (days < 30) return `${days}d ago`;
  const months = Math.round(days / 30);
  if (months < 12) return `${months}mo ago`;
  return `${Math.round(months / 12)}y ago`;
}

export default function VersionRailway({
  versions,
  selectedRef,
  selectedSha,
}: {
  versions: RegistryVersion[];
  selectedRef?: string;
  selectedSha?: string;
}) {
  if (versions.length === 0) {
    return (
      <div className="text-sm text-gray-400">
        No branches or tags found in this registry's clone.
      </div>
    );
  }
  // Cap the visible window to ~10 versions (each node ≈ 68px) and scroll within
  // it, so a repo with a long tag history doesn't push the rest of the page out
  // of view. `pr-1` keeps the scrollbar off the node cards.
  return (
    <ol 
      className="max-h-[42rem] overflow-y-auto pr-1 text-sm"
      tabIndex={0}
      aria-label="Version history timeline"
    >
      {versions.map((v, i) => {
        const isLast = i === versions.length - 1;
        const selected =
          (!!selectedRef && v.ref === selectedRef) ||
          (!!selectedSha && v.sha === selectedSha);
        return (
          <li key={`${v.isTag ? "tag" : "branch"}:${v.ref}`} className="flex gap-3">
            {/* rail: dot threaded on a line that reaches the next node */}
            <div className="flex w-3 shrink-0 flex-col items-center">
              <span
                className={`mt-1.5 h-2.5 w-2.5 shrink-0 rounded-full ring-2 ring-white ${
                  selected
                    ? "bg-emerald-500"
                    : v.isTag
                      ? "bg-amber-400"
                      : "bg-gray-300"
                }`}
              />
              {!isLast && <span className="mt-1 w-px flex-1 bg-gray-200" />}
            </div>

            {/* node */}
            <div
              className={`mb-3 min-w-0 flex-1 rounded-lg px-3 py-2 ${
                selected ? "bg-emerald-50 ring-1 ring-inset ring-emerald-200" : "bg-gray-50"
              }`}
            >
              <div className="flex flex-wrap items-center gap-1.5">
                <Pill tone={v.isTag ? "amber" : "blue"}>{v.isTag ? "tag" : "branch"}</Pill>
                <span
                  className={`font-medium ${selected ? "text-emerald-800" : "text-gray-800"}`}
                >
                  {v.ref}
                </span>
                <Mono title={v.sha}>{short(v.sha)}</Mono>
                {v.current && <Pill tone="gray">default</Pill>}
                {selected && <Pill tone="green">current</Pill>}
                <span className="ml-auto text-xs text-gray-400">{relTime(v.time)}</span>
              </div>
              {v.subject && (
                <p className="mt-1 truncate text-xs text-gray-500" title={v.subject}>
                  {v.subject}
                </p>
              )}
            </div>
          </li>
        );
      })}
    </ol>
  );
}
