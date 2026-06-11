import { Library } from "lucide-react";
import { api, useFetch } from "../api";
import {
  Badge,
  EmptyState,
  ErrorBox,
  Loading,
  PageHead,
  RefreshButton,
  Prompt,
  SkillRowItem,
  Tag,
} from "../components/qvr";
import { relTime } from "../lib/format";

export default function Registries() {
  const { data, error, loading, reload } = useFetch(api.registries, "registries");

  return (
    <>
      <PageHead
        title="Registries"
        sub="Plain Git repos. No central server."
        actions={<RefreshButton onClick={reload} busy={loading} />}
      />
      {loading && <Loading />}
      {error && <ErrorBox message={error} />}
      {data && data.length === 0 && (
        <>
          <EmptyState title="no registries">
            any git repo with skills in it can be a registry. add one:
          </EmptyState>
          <div style={{ maxWidth: 420, margin: "0 auto" }}>
            <Prompt command="qvr registry add <git-url>" />
          </div>
        </>
      )}
      <div>
        {(data ?? []).map((r) => (
          <SkillRowItem
            key={r.name}
            to={`/registries/${encodeURIComponent(r.name)}`}
            lead={<Library size={16} style={{ color: "var(--text-faint)", flex: "none" }} />}
            name={
              <>
                {r.name}{" "}
                {r.has_upstream_changes && (
                  <Badge tone="accent" dot>
                    updates
                  </Badge>
                )}
                {r.error && <Badge tone="danger">error</Badge>}
              </>
            }
            desc={<span style={{ color: "var(--link)" }}>{r.error || r.url}</span>}
            right={
              <>
                <Tag>{r.skill_count} skills</Tag>
                <span className="qvr-skillrow__reg">
                  fetched{" "}
                  {r.last_fetched && !r.last_fetched.startsWith("0001")
                    ? relTime(r.last_fetched)
                    : "never"}
                </span>
              </>
            }
          />
        ))}
      </div>
    </>
  );
}
