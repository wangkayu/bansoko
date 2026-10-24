import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { FolderOpen } from "lucide-react";
import { useInRouterContext } from "react-router-dom";
import { locateFileManagerAnime } from "../../lib/api/client";
import { fileManagerKeys } from "../../lib/queryKeys";
import { t } from "../../lib/i18n";
import { Button } from "../../ui/base";
import { useRouteDialogs } from "../../shell/routeDialogs";

/** The anime settings' "open in file manager": locates the folder holding this anime's files;
 *  还没有文件时从根目录打开。文件管理由路由承载，没有路由时（独立渲染）不显示。 */
export function OpenInFileManagerButton({ animeID }: { animeID: string }) {
  return useInRouterContext() ? <RoutedButton animeID={animeID} /> : null;
}

function RoutedButton({ animeID }: { animeID: string }) {
  const routeDialogs = useRouteDialogs();
  const queryClient = useQueryClient();
  const [busy, setBusy] = useState(false);
  async function open() {
    if (busy) return;
    setBusy(true);
    try {
      const location = await queryClient.fetchQuery({
        queryKey: fileManagerKeys.location(animeID),
        queryFn: () => locateFileManagerAnime(animeID),
        staleTime: 0,
      });
      routeDialogs.open({ tool: "files", path: location.path || undefined });
    } catch {
      // When the location cannot be found (no files yet, or the location is currently unavailable), the file manager still opens, browsing from the root.
      routeDialogs.open({ tool: "files" });
    } finally {
      setBusy(false);
    }
  }
  return (
    <Button size="sm" loading={busy} onClick={() => void open()}>
      <FolderOpen className="tw:size-4" aria-hidden="true" />
      {t("fileManager.openFromAnime")}
    </Button>
  );
}
