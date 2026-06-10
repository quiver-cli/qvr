import type { ReactNode } from "react";
import { ChevronRight } from "lucide-react";
import { Link } from "react-router-dom";

// SkillRowItem — the kit's interactive list row: badge | name+desc | right
// meta | chevron. Used for skills, registries, and needs-attention lists.
export function SkillRowItem({
  to,
  lead,
  name,
  desc,
  right,
  chevron = true,
}: {
  to?: string;
  lead?: ReactNode;
  name: ReactNode;
  desc?: ReactNode;
  right?: ReactNode;
  chevron?: boolean;
}) {
  const body = (
    <>
      {lead}
      <div style={{ minWidth: 0, flex: 1 }}>
        <span className="qvr-skillrow__name">{name}</span>
        {desc != null && <div className="qvr-skillrow__desc">{desc}</div>}
      </div>
      {right}
      {chevron && to != null && <ChevronRight className="chev" />}
    </>
  );
  if (to != null) {
    return (
      <Link to={to} className="qvr-skillrow">
        {body}
      </Link>
    );
  }
  return (
    <div className="qvr-skillrow" style={{ cursor: "default" }}>
      {body}
    </div>
  );
}
