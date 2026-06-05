import { Link } from "react-router-dom";
import { api, useFetch } from "../api";
import {
  Empty,
  ErrorBox,
  fmtTime,
  Loading,
  Mono,
  PageHeader,
  Pill,
  Table,
  Td,
  Th,
} from "../components/ui";

export default function Registries() {
  const { data, error, loading } = useFetch(api.registries, "registries");

  return (
    <>
      <PageHeader
        title="Registries"
        subtitle="Global · shared across all projects. Click a registry to see its skills and versions."
      />
      {loading && <Loading />}
      {error && <ErrorBox message={error} />}
      {data && data.length === 0 && (
        <Empty>
          No registries configured. Add one with{" "}
          <code className="rounded bg-gray-100 px-1.5 py-0.5">qvr registry add &lt;url&gt;</code>.
        </Empty>
      )}
      {data && data.length > 0 && (
        <Table
          head={
            <tr>
              <Th>Name</Th>
              <Th>URL</Th>
              <Th>Skills</Th>
              <Th>Last fetched</Th>
            </tr>
          }
        >
          {data.map((r) => (
            <tr key={r.name} className="hover:bg-gray-50">
              <Td>
                <Link
                  to={`/registries/${encodeURIComponent(r.name)}`}
                  className="font-medium text-blue-600 hover:underline"
                >
                  {r.name}
                </Link>
                {r.has_upstream_changes && (
                  <Pill tone="amber">
                    <span className="ml-1">updates</span>
                  </Pill>
                )}
                {r.error && <div className="mt-1 text-xs text-red-600">{r.error}</div>}
              </Td>
              <Td>
                <Mono>{r.url}</Mono>
              </Td>
              <Td>{r.skill_count}</Td>
              <Td>{r.last_fetched && !r.last_fetched.startsWith("0001") ? fmtTime(r.last_fetched) : "never"}</Td>
            </tr>
          ))}
        </Table>
      )}
    </>
  );
}
