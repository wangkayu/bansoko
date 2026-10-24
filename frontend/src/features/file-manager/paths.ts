// Absolute-path helpers; paths are always "/"-separated. The file manager handles only absolute
//   paths: there is no "location root" layer anymore, locations come from the server's quick
//   entries (mounts).

export const ROOT = "/";

/** Strips a trailing "/" but keeps the root directory itself. */
function trimTrailingSlash(path: string): string {
  const trimmed = path.replace(/\/+$/, "");
  return trimmed === "" ? ROOT : trimmed;
}

/** Normalizes a user-entered absolute path: collapses repeated "/" and strips a trailing "/"; a relative path returns undefined. */
export function normalizePath(raw: string): string | undefined {
  const trimmed = raw.trim();
  if (!trimmed.startsWith(ROOT)) return undefined;
  return trimTrailingSlash(trimmed.replace(/\/{2,}/g, ROOT));
}

export function parentOf(path: string): string {
  const clean = trimTrailingSlash(path);
  if (clean === ROOT) return ROOT;
  const index = clean.lastIndexOf("/");
  return index <= 0 ? ROOT : clean.slice(0, index);
}

export function baseName(path: string): string {
  const clean = trimTrailingSlash(path);
  return clean === ROOT ? ROOT : clean.slice(clean.lastIndexOf("/") + 1);
}

export function joinPath(parent: string, name: string): string {
  return parent === ROOT ? `${ROOT}${name}` : `${trimTrailingSlash(parent)}/${name}`;
}

/** The directory names of an absolute path, level by level; the root is []. */
export function segmentsOf(path: string): string[] {
  return trimTrailingSlash(path).split("/").filter(Boolean);
}

/** The absolute path assembled level by level from the root. */
export function ancestorOf(path: string, depth: number): string {
  return depth <= 0 ? ROOT : `${ROOT}${segmentsOf(path).slice(0, depth).join("/")}`;
}
