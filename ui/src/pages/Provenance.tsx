import { api, useFetch } from "../api";
import {
  Empty,
  ErrorBox,
  Loading,
  Mono,
  PageHeader,
  short,
  StatusPill,
  Table,
  Td,
  Th,
} from "../components/ui";

export default function Provenance() {
  const { data, error, loading } = useFetch(api.provenance, "provenance");

  return (
    <>
      <PageHeader
        title="Provenance"
        subtitle="Where each skill came from, what it's pinned to, and what's verified."
      />
      {loading && <Loading />}
      {error && <ErrorBox message={error} />}
      {data && data.length === 0 && <Empty>No installed skills.</Empty>}
      {data && data.length > 0 && (
        <Table
          head={
            <tr>
              <Th>Skill</Th>
              <Th>Resolved</Th>
              <Th>Tree OID</Th>
              <Th>Signature</Th>
              <Th>Scan</Th>
              <Th>Status</Th>
            </tr>
          }
        >
          {data.map((p) => (
            <tr key={p.name} className="hover:bg-[#f7f9f8]">
              <Td>
                <span className="font-medium">{p.name}</span>
                {p.source && (
                  <div className="text-xs text-[#708078]">
                    <Mono>{p.source}</Mono>
                  </div>
                )}
              </Td>
              <Td title={p.resolved}>
                <Mono>{short(p.resolved)}</Mono>
                {p.requested && <span className="ml-1 text-xs text-[#708078]">@{p.requested}</span>}
              </Td>
              <Td title={p.treeOID}>
                <Mono>{short(p.treeOID)}</Mono>
              </Td>
              <Td>
                <StatusPill value={p.signatureStatus} />
                {p.signer && <div className="text-xs text-[#708078]">{p.signer}</div>}
              </Td>
              <Td>
                <StatusPill value={p.scanDecision || "none"} />
              </Td>
              <Td>
                <span className="text-xs text-[#52615a]">{p.status}</span>
              </Td>
            </tr>
          ))}
        </Table>
      )}
    </>
  );
}
