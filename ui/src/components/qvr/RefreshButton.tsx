import { RefreshCw } from "lucide-react";
import { Button } from "./Button";

// RefreshButton — the per-view reload control every page mounts in its
// PageHead actions slot. It re-runs that view's loaders (scoped to whatever
// the view is currently showing); it does not rescan agent stores — that's
// the Sessions page's discover button.
export function RefreshButton({ onClick, busy }: { onClick: () => void; busy?: boolean }) {
  return (
    <Button size="sm" variant="ghost" onClick={onClick} disabled={busy} leftIcon={<RefreshCw size={13} />}>
      refresh
    </Button>
  );
}
