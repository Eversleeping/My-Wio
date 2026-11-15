import { type WorkspaceFile } from "../types";

export type FileTreeNode = { name: string; path: string; kind: WorkspaceFile["kind"]; size?: number; children: FileTreeNode[] };

export function buildFileTree(files: WorkspaceFile[]): FileTreeNode[] {
  type MutableNode = Omit<FileTreeNode, "children"> & { children: Map<string, MutableNode> };
  const root = new Map<string, MutableNode>();
  for (const entry of files) {
    const parts = entry.path.split("/").filter(Boolean);
    let children = root;
    for (let index = 0; index < parts.length; index++) {
      const name = parts[index];
      const path = parts.slice(0, index + 1).join("/");
      const last = index === parts.length - 1;
      let node = children.get(name);
      if (!node) {
        node = { name, path, kind: last ? entry.kind : "directory", size: last ? entry.size : undefined, children: new Map() };
        children.set(name, node);
      } else if (last) {
        node.kind = entry.kind;
        node.size = entry.size;
      }
      children = node.children;
    }
  }
  const convert = (items: Map<string, MutableNode>): FileTreeNode[] => Array.from(items.values()).sort((left, right) => left.kind === right.kind ? left.name.localeCompare(right.name) : left.kind === "directory" ? -1 : 1).map(node => ({ ...node, children: convert(node.children) }));
  return convert(root);
}
