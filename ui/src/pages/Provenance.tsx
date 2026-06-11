import { api, scopeToken, useFetch } from "../api";
import {
  EmptyState,
  ErrorBox,
  Loading,
  PageHead,
  RefreshButton,
  StatusBadge,
  Table,
  Tag,
  Td,
  Th,
} from "../components/qvr";
import { short } from "../lib/format";

export default function Provenance() {
  const { data, error, loading, reload } = useFetch(api.provenance, `provenance:${scopeToken()}`);

  return (
    <>
      <PageHead
        title="Provenance"
        sub="What's pinned, who signed it, what the gate said."
        actions={<RefreshButton onClick={reload} busy={loading} />}
      />
      {loading && <Loading />}
      {error && <ErrorBox message={error} />}
      {data && data.length === 0 && (
        <EmptyState title="nothing pinned">
          provenance appears per skill once the lock has entries.
        </EmptyState>
      )}
      {data && data.length > 0 && (
        <Table
          head={
            <tr>
              <Th>skill</Th>
              <Th>resolved</Th>
              <Th>tree oid</Th>
              <Th>signature</Th>
              <Th>scan</Th>
              <Th>status</Th>
            </tr>
          }
        >
          {data.map((p) => (
            <tr key={p.name}>
              <Td>
                <span style={{ fontWeight: "var(--weight-medium)" as never }}>{p.name}</span>
                {p.source && (
                  <div className="qvr-scan__scanner" style={{ marginTop: 2 }}>
                    {p.source}
                  </div>
                )}
              </Td>
              <Td>
                <Tag lead="#" title={p.resolved}>
                  {short(p.resolved)}
                </Tag>
                {p.requested && (
                  <span className="qvr-scan__scanner" style={{ marginLeft: 4 }}>
                    @{p.requested}
                  </span>
                )}
              </Td>
              <Td>
                <Tag lead="#" title={p.treeOID}>
                  {short(p.treeOID)}
                </Tag>
              </Td>
              <Td>
                <StatusBadge value={p.signatureStatus} />
                {p.signer && (
                  <div className="qvr-scan__scanner" style={{ marginTop: 2 }}>
                    {p.signer}
                  </div>
                )}
              </Td>
              <Td>
                <StatusBadge value={p.scanDecision || "none"} />
              </Td>
              <Td muted>{p.status}</Td>
            </tr>
          ))}
        </Table>
      )}
    </>
  );
}
