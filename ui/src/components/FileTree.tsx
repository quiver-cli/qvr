import { useMemo, useState } from "react";

// FileTree renders a flat list of skill-relative paths (the shape both
// SkillInfo.files and RegistrySkillDetail.files arrive in) as a nested,
// collapsible directory tree. Dependency-free and styled to match the rest of
// the dashboard: the same ▸/▾ disclosure glyph the registry version list uses,
// muted mono filenames, light hover. Optionally annotates files with a scan
// finding count so a live scan can flag exactly which files tripped a rule.

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
  // Optional path → finding count, surfaced as a red badge on flagged files.
  findings?: Record<string, number>;
}) {
  const tree = useMemo(() => buildTree(paths), [paths]);
  if (paths.length === 0) {
    return <div className="text-sm text-gray-400">No files.</div>;
  }
  return (
    <ul className="space-y-0.5 text-sm">
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
  const pad = { paddingLeft: `${depth * 14}px` };

  if (node.isDir) {
    return (
      <li>
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          style={pad}
          className="flex w-full items-center gap-1.5 rounded px-1 py-0.5 text-left hover:bg-gray-50"
        >
          <span className="w-3 shrink-0 text-gray-400">{open ? "▾" : "▸"}</span>
          <span className="font-medium text-gray-700">{node.name}</span>
          <span className="text-xs text-gray-300">/</span>
        </button>
        {open && (
          <ul className="space-y-0.5">
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
      <div
        style={pad}
        className="flex items-center gap-1.5 rounded px-1 py-0.5 hover:bg-gray-50"
        title={node.path}
      >
        <span className="w-3 shrink-0" />
        <span className="font-mono text-[0.8125rem] text-gray-700">{node.name}</span>
        <span className="text-[0.6875rem] uppercase tracking-wide text-gray-300">
          {fileKind(node.name)}
        </span>
        {count > 0 && (
          <span className="ml-auto inline-flex items-center rounded-md bg-red-100 px-1.5 text-[0.6875rem] font-medium text-red-700 ring-1 ring-inset ring-red-600/20">
            {count}
          </span>
        )}
      </div>
    </li>
  );
}
