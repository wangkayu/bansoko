import type { FileManagerOperationView, FileManagerRoot } from "../../lib/api/client";

export function operationInFlight(view: FileManagerOperationView | undefined): boolean {
  return view?.state === "ready" || view?.state === "running";
}

/** True when an absolute path is the root itself or lies inside it. */
export function insideRoot(root: Pick<FileManagerRoot, "path">, path: string): boolean {
  return path === root.path || path.startsWith(`${root.path}/`);
}

/** The deepest available root that contains the path: the browsing location's root. */
export function activeRootOf(roots: readonly FileManagerRoot[], path: string | undefined): FileManagerRoot | undefined {
  if (path === undefined) return undefined;
  return roots.filter((root) => root.available && insideRoot(root, path)).sort((a, b) => b.path.length - a.path.length)[0];
}
