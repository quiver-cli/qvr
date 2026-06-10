import { useMemo, useState } from "react";
import { ChevronDown, ChevronRight, FileText, Folder } from "lucide-react";

// FileTree renders a flat list of skill-relative paths (the shape both
// SkillInfo.files and RegistrySkillDetail.files arrive in) as a nested,
// collapsible directory tree in the kit's .qvr-frow rhythm: mono names,
// uppercase kind labels, hairline row separation. Optionally annotates files
// with a scan finding count so a live scan can flag exactly which files
// tripped a rule.

export interface TreeNode {
  name: string;
  path: string; // full skill-relative path
  isDir: boolean;
  children: TreeNode[];
}

// buildTree folds a flat path list into a sorted node tree: directories first,
// then files, each alphabetical. Intermediate directories are synthesized even
// when no path names them directly.
export function buildTree(paths: string[]): TreeNode[] {
  const root: TreeNode = { name: "", path: "", isDir: true, children: [] };

  for (const p of paths) {
    const parts = p.split("/").filter(Boolean);
    let cursor = root;
    parts.forEach((part, i) => {
      const isLeaf = i === parts.length - 1;
      const full = parts.slice(0, i + 1).join("/");
      let child = cursor.children.find((c) => c.name === part && c.isDir !== isLeaf);
      if (!child) {
        child = { name: part, path: full, isDir: !isLeaf, children: [] };
        cursor.children.push(child);
      }
      cursor = child;
    });
  }

  const sortNodes = (nodes: TreeNode[]) => {
    nodes.sort((a, b) => {
      if (a.isDir !== b.isDir) return a.isDir ? -1 : 1;
      return a.name.localeCompare(b.name);
    });
    for (const n of nodes) if (n.isDir) sortNodes(n.children);
  };
  sortNodes(root.children);
  return root.children;
}

// fileKind classifies a filename into a coarse language/category, reused by the
// scan panel's inventory breakdown. Keep the set small and obvious.
export function fileKind(name: string): string {
  const ext = name.includes(".") ? name.slice(name.lastIndexOf(".") + 1).toLowerCase() : "";
  switch (ext) {
    case "md":
    case "markdown":
      return "markdown";
    case "py":
      return "python";
    case "sh":
    case "bash":
    case "zsh":
      return "shell";
    case "js":
    case "jsx":
    case "ts":
    case "tsx":
    case "mjs":
    case "cjs":
      return "javascript";
    case "json":
      return "json";
    case "yaml":
    case "yml":
      return "yaml";
    case "toml":
      return "toml";
    case "txt":
      return "text";
    case "":
      return "other";
    default:
      return ext;
  }
}

export default function FileTree({
  paths,
  findings,
}: {
  paths: string[];
  // Optional path → finding count, surfaced as a danger badge on flagged files.
  findings?: Record<string, number>;
}) {
  const tree = useMemo(() => buildTree(paths), [paths]);
  if (paths.length === 0) {
    return <p className="qvr-sub">no files.</p>;
  }
  return (
    <ul style={{ listStyle: "none", margin: 0, padding: 0 }}>
      {tree.map((n) => (
        <TreeRow key={n.path} node={n} depth={0} findings={findings} />
      ))}
    </ul>
  );
}

function TreeRow({
  node,
  depth,
  findings,
}: {
  node: TreeNode;
  depth: number;
  findings?: Record<string, number>;
}) {
  const [open, setOpen] = useState(true);
  const pad = { paddingLeft: `${depth * 16}px` };

  if (node.isDir) {
    return (
      <li>
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          aria-expanded={open}
          className="qvr-frow"
          style={{
            ...pad,
            width: "100%",
            background: "none",
            border: "none",
            cursor: "pointer",
            textAlign: "left",
          }}
        >
          {open ? <ChevronDown /> : <ChevronRight />}
          <Folder />
          <span className="qvr-frow__name">{node.name}</span>
        </button>
        {open && (
          <ul style={{ listStyle: "none", margin: 0, padding: 0 }}>
            {node.children.map((c) => (
              <TreeRow key={c.path} node={c} depth={depth + 1} findings={findings} />
            ))}
          </ul>
        )}
      </li>
    );
  }

  const count = findings?.[node.path] ?? 0;
  return (
    <li>
      <div className="qvr-frow" style={pad} title={node.path}>
        <FileText />
        <span className="qvr-frow__name">{node.name}</span>
        <span className="qvr-frow__kind">{fileKind(node.name)}</span>
        {count > 0 && (
          <span className="qvr-frow__r">
            <span className="qvr-badge qvr-badge--danger">{count}</span>
          </span>
        )}
      </div>
    </li>
  );
}
