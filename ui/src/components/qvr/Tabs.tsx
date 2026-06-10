import type { ReactNode } from "react";

export interface TabItem {
  id: string;
  label: ReactNode;
  icon?: ReactNode;
  count?: number;
}

// Tabs — lime-underlined active tab with optional mono count chips.
export function Tabs({
  items,
  value,
  onChange,
}: {
  items: TabItem[];
  value: string;
  onChange: (id: string) => void;
}) {
  return (
    <div className="qvr-tabs" role="tablist">
      {items.map((it) => (
        <button
          key={it.id}
          type="button"
          role="tab"
          className="qvr-tab"
          aria-selected={it.id === value}
          onClick={() => onChange(it.id)}
        >
          {it.icon}
          {it.label}
          {it.count != null && <span className="qvr-tab__count">{it.count}</span>}
        </button>
      ))}
    </div>
  );
}
