import type { FileManagerMount, FileManagerMountList, FileManagerOperationView } from "../../lib/api/client";
import { t } from "../../lib/i18n";

export function operationInFlight(view: FileManagerOperationView | undefined): boolean {
  return view?.state === "ready" || view?.state === "running";
}

/** Display names of the quick entries: the filesystem root, the download directory (with connection name), and the anime (with its title). */
export function mountLabel(mount: FileManagerMount): string {
  if (mount.kind === "anime") return mount.label ?? mount.path;
  if (mount.kind === "download") {
    return mount.label ? t("fileManager.mount.downloadNamed", { name: mount.label }) : t("fileManager.mount.download");
  }
  return t("fileManager.mount.root");
}

/** The anime name on the lock badge: the anime quick entry is already loaded, so this never re-requests the anime title. */
export function animeTitleIndex(mounts: FileManagerMountList | undefined): Map<string, string> {
  const index = new Map<string, string>();
  for (const mount of mounts?.mounts ?? []) {
    if (mount.kind === "anime" && mount.managed_anime_id && mount.label) index.set(mount.managed_anime_id, mount.label);
  }
  return index;
}
