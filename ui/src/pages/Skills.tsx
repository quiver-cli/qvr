import { Link } from "react-router-dom";
import { api, useFetch } from "../api";
import {
  Empty,
  ErrorBox,
  Loading,
  Mono,
  PageHeader,
  Pill,
  short,
  Table,
  Td,
  Th,
} from "../components/ui";

export default function Skills() {
  const { data, error, loading } = useFetch(api.skills, "skills");

  return (
    <>
      <PageHeader title="Skills" subtitle="Installed skills recorded in the lock file." />
      {loading && <Loading />}
      {error && <ErrorBox message={error} />}
      {data && data.length === 0 && <Empty>No installed skills.</Empty>}
      {data && data.length > 0 && (
        <Table
          head={
            <tr>
              <Th>Skill</Th>
              <Th>Registry</Th>
              <Th>Version</Th>
              <Th>Targets</Th>
              <Th>Mode</Th>
              <Th>Status</Th>
            </tr>
          }
        >
          {data.map((s) => (
            <tr key={`${s.scope ?? ""}/${s.name}`} className="hover:bg-[#f7f9f8]">
              <Td>
                <Link
                  to={`/skills/${encodeURIComponent(s.name)}`}
                  className="font-medium text-[#2f765d] hover:underline"
                >
                  {s.name}
                </Link>
              </Td>
              <Td>{s.registry || "—"}</Td>
              <Td>
                {s.ref || "—"}
                {s.commit ? (
                    <span className="ml-1 text-xs text-[#708078]">
                    <Mono>{short(s.commit)}</Mono>
                  </span>
                ) : null}
              </Td>
              <Td>
                <div className="flex flex-wrap gap-1">
                  {(s.targets ?? []).length === 0
                    ? "—"
                    : s.targets!.map((t) => (
                        <Pill key={t} tone="blue">
                          {t}
                        </Pill>
                      ))}
                </div>
              </Td>
              <Td>{s.mode ? <Pill tone="amber">{s.mode}</Pill> : "shared"}</Td>
              <Td>
                {s.disabled ? <Pill tone="gray">disabled</Pill> : <Pill tone="green">enabled</Pill>}
              </Td>
            </tr>
          ))}
        </Table>
      )}
    </>
  );
}
