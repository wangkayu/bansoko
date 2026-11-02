import { useEffect, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Folder, HardDrive } from "lucide-react";
import { addFileManagerRoot, removeFileManagerRoot, toUserMessage, type FileManagerRoot } from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { fileManagerKeys } from "../../lib/queryKeys";
import { Alert, Button, Dialog, Input } from "../../ui/base";
import { normalizePath } from "./paths";

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
