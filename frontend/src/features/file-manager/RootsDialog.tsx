import { useEffect, useMemo, useState } from "react";
import { useInfiniteQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { ArrowUp, ChevronRight, Folder, HardDrive } from "lucide-react";
import {
  addFileManagerRoot,
  listFileManagerRootPickerEntries,
  removeFileManagerRoot,
  toUserMessage,
  type FileManagerRoot,
} from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { fileManagerKeys } from "../../lib/queryKeys";
import { Alert, Button, Dialog, Input } from "../../ui/base";
import { normalizePath, parentOf, ROOT } from "./paths";

// RootsDialog — the owner manages the library directories. Adding one
// authorizes file management below it; removing one never deletes files on
// disk and never strands operations already admitted. The browsing layer
// outside this dialog only ever reads the list.
export function RootsDialog({
  roots,
  activeRootID,
  prefill,
  onRemoved,
  onClose,
}: {
  roots: readonly FileManagerRoot[];
  /** The root being browsed (the deepest one holding the location): nested roots containing it stay unmarked. */
  activeRootID: string | undefined;
  /** A path to offer for adding, from the outside-roots state. */
  prefill?: string;
  onRemoved: (removedPath: string) => void;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const [draft, setDraft] = useState(prefill ?? "");
  useEffect(() => {
    if (prefill) setDraft(prefill);
  }, [prefill]);

  // The tree starts where the typed path points (or at the filesystem root)
  // and only reads subdirectory names; picking one fills the same draft the
  // manual input uses, and adding stays the dialog's single write.
  const [browsing, setBrowsing] = useState(false);
  const [browsePath, setBrowsePath] = useState(ROOT);
  const picker = useInfiniteQuery({
    queryKey: fileManagerKeys.rootPicker(browsePath),
    queryFn: ({ pageParam }) => listFileManagerRootPickerEntries(browsePath, pageParam),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (lastPage) => lastPage.next_cursor ?? undefined,
    enabled: browsing,
  });
  const folders = useMemo(() => picker.data?.pages.flatMap((page) => page.entries) ?? [], [picker.data]);
  const truncated = picker.data?.pages.some((page) => page.truncated) ?? false;

  function toggleBrowsing() {
    if (browsing) {
      setBrowsing(false);
      return;
    }
    setBrowsePath(normalizePath(draft) ?? ROOT);
    setBrowsing(true);
  }

  function selectFolder(path: string) {
    setDraft(path);
    setBrowsing(false);
    toast.success(t("fileManager.roots.selected", { path }));
  }

  const add = useMutation({
    mutationFn: () => addFileManagerRoot(normalizePath(draft) ?? draft.trim()),
    onSuccess: (root) => {
      toast.success(t("fileManager.roots.added", { path: root.path }));
      setDraft("");
    },
    onSettled: () => void queryClient.invalidateQueries({ queryKey: fileManagerKeys.all }),
  });
  const remove = useMutation({
    mutationFn: (root: FileManagerRoot) => removeFileManagerRoot(root.id),
    onSuccess: (_result, root) => {
      toast.success(t("fileManager.roots.removed", { path: root.path }));
      onRemoved(root.path);
    },
    onError: (cause) => toast.error(toUserMessage(cause, t("fileManager.roots.removeFailed"))),
    onSettled: () => void queryClient.invalidateQueries({ queryKey: fileManagerKeys.all }),
  });

  return (
    <Dialog
      open
      tier="M"
      title={t("fileManager.roots.title")}
      description={t("fileManager.roots.sub")}
      onRequestClose={add.isPending || remove.isPending ? () => {} : onClose}
      closeDisabled={add.isPending || remove.isPending}
      footer={
        <>
          <span className="tw:mr-auto" />
          <Button variant="primary" onClick={onClose}>
            {t("fileManager.roots.done")}
          </Button>
        </>
      }
    >
      <div className="tw:grid tw:gap-3">
        <form
          className="tw:flex tw:items-center tw:gap-2"
          onSubmit={(event) => {
            event.preventDefault();
            if (draft.trim()) add.mutate();
          }}
        >
          <Input
            aria-label={t("fileManager.roots.pathLabel")}
            placeholder={t("fileManager.roots.placeholder")}
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
            autoComplete="off"
            spellCheck={false}
          />
          <Button size="sm" variant="secondary" type="submit" loading={add.isPending} disabled={!draft.trim()}>
            {t("fileManager.roots.add")}
          </Button>
        </form>
        {add.error ? <Alert tone="error" title={toUserMessage(add.error, t("fileManager.roots.addFailed"))} /> : null}
        <div className="tw:grid tw:justify-items-start tw:gap-1.5">
          <Button size="sm" variant={browsing ? "ghost" : "secondary"} onClick={toggleBrowsing}>
            {t(browsing ? "fileManager.roots.browseHide" : "fileManager.roots.browse")}
          </Button>
          {browsing ? (
            <p className="tw:m-0 tw:text-xs tw:text-muted-foreground">{t("fileManager.roots.browseHint")}</p>
          ) : null}
        </div>
        {browsing ? (
          <section
            aria-label={t("fileManager.roots.browseAria")}
            className="tw:grid tw:gap-2 tw:rounded-lg tw:border tw:border-border tw:p-2.5"
          >
            <div className="tw:flex tw:min-w-0 tw:flex-wrap tw:items-center tw:gap-1.5">
              <Button
                size="sm"
                variant="ghost"
                disabled={browsePath === ROOT}
                onClick={() => setBrowsePath(parentOf(browsePath))}
              >
                <ArrowUp className="tw:size-4 tw:flex-none" aria-hidden="true" />
                {t("fileManager.roots.up")}
              </Button>
              <span className="tw:min-w-0 tw:flex-1 tw:break-all tw:text-xs tw:text-muted-foreground">{browsePath}</span>
            </div>
            {picker.isPending ? (
              <p className="tw:m-0 tw:text-xs tw:text-muted-foreground">{t("fileManager.roots.browseLoading")}</p>
            ) : picker.isError ? (
              <Alert tone="error" title={toUserMessage(picker.error, t("fileManager.roots.browseFailed"))} />
            ) : folders.length === 0 ? (
              <p className="tw:m-0 tw:text-xs tw:text-muted-foreground">{t("fileManager.roots.browseEmpty")}</p>
            ) : (
              <ul className="tw:m-0 tw:grid tw:list-none tw:gap-1 tw:p-0">
                {folders.map((folder) => (
                  <li key={folder.path} className="tw:flex tw:min-w-0 tw:items-center tw:gap-1">
                    <Button
                      variant="ghost"
                      className="tw:min-w-0 tw:flex-1 tw:justify-start tw:text-left"
                      onClick={() => setBrowsePath(folder.path)}
                    >
                      <Folder className="tw:size-4 tw:flex-none tw:text-muted-foreground" aria-hidden="true" />
                      <span className="tw:min-w-0 tw:flex-1 tw:truncate">{folder.name}</span>
                      <ChevronRight className="tw:size-4 tw:flex-none tw:text-muted-foreground" aria-hidden="true" />
                    </Button>
                    <Button size="sm" variant="ghost" className="tw:flex-none" onClick={() => selectFolder(folder.path)}>
                      {t("fileManager.roots.select")}
                    </Button>
                  </li>
                ))}
              </ul>
            )}
            {picker.hasNextPage ? (
              <div>
                <Button
                  size="sm"
                  variant="ghost"
                  loading={picker.isFetchingNextPage}
                  onClick={() => void picker.fetchNextPage()}
                >
                  {t("fileManager.loadMore")}
                </Button>
              </div>
            ) : null}
            {truncated ? (
              <p className="tw:m-0 tw:text-xs tw:text-muted-foreground">{t("fileManager.roots.browseTruncated")}</p>
            ) : null}
            <div className="tw:border-t tw:border-border tw:pt-2">
              <Button size="sm" variant="secondary" onClick={() => selectFolder(browsePath)}>
                {t("fileManager.roots.selectCurrent")}
              </Button>
            </div>
          </section>
        ) : null}
        {roots.length === 0 ? (
          <p className="tw:m-0 tw:text-xs tw:text-muted-foreground">{t("fileManager.roots.empty")}</p>
        ) : (
          <ul className="tw:m-0 tw:grid tw:list-none tw:gap-1 tw:p-0">
            {roots.map((root) => {
              const current = root.id === activeRootID;
              return (
                <li
                  key={root.id}
                  className="tw:flex tw:items-center tw:gap-2.5 tw:rounded-lg tw:border tw:border-border tw:px-2.5 tw:py-1.5"
                >
                  {root.available ? (
                    <Folder className="tw:size-4 tw:flex-none tw:text-muted-foreground" aria-hidden="true" />
                  ) : (
                    <HardDrive className="tw:size-4 tw:flex-none tw:text-muted-foreground" aria-hidden="true" />
                  )}
                  <span className="tw:grid tw:min-w-0 tw:flex-1 tw:gap-0.5">
                    <span className="tw:flex tw:min-w-0 tw:flex-wrap tw:items-baseline tw:gap-x-2 tw:text-sm tw:break-words">
                      {root.label}
                      {current ? (
                        <span className="tw:rounded-full tw:border tw:border-border tw:px-1.5 tw:text-badge tw:text-muted-foreground">
                          {t("fileManager.roots.current")}
                        </span>
                      ) : null}
                    </span>
                    <span className="tw:break-all tw:text-xs tw:text-muted-foreground">{root.path}</span>
                  </span>
                  <Button
                    size="sm"
                    variant="ghost"
                    loading={remove.isPending && remove.variables?.id === root.id}
                    onClick={() => remove.mutate(root)}
                  >
                    {t("fileManager.roots.remove")}
                  </Button>
                </li>
              );
            })}
          </ul>
        )}
        <p className="tw:m-0 tw:text-xs tw:text-muted-foreground">{t("fileManager.roots.hint")}</p>
      </div>
    </Dialog>
  );
}
