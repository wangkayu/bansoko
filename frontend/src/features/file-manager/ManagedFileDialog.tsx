import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Lock } from "lucide-react";
import { getFileManagerEntry, releaseFileManagerEntry, toUserMessage, type FileManagerEntryDetail } from "../../lib/api/client";
import { formatBytes, formatDateTime } from "../../lib/labels";
import { t, tryT } from "../../lib/i18n";
import { fileManagerKeys } from "../../lib/queryKeys";
import { Alert, Button, Dialog, StatePill } from "../../ui/base";
import { DescriptionList } from "../../ui/DescriptionList";
import { useIdempotencyKeys } from "./batch";

// ManagedFileDialog — the read-only detail of a locked entry: the state, the
// work that manages it, its companions and the activity links. The only write
// is 解除管理, which ends the management in place; the file itself is never
// moved, copied, renamed or deleted from here.
export function ManagedFileDialog({
  path,
  onOpenAnime,
  onOpenRecord,
  onClose,
}: {
  path: string;
  onOpenAnime: (managedAnimeID: string) => void;
  onOpenRecord: (acquisitionID: string) => void;
  onClose: () => void;
}) {
  const query = useQuery({ queryKey: fileManagerKeys.entry(path), queryFn: () => getFileManagerEntry(path) });
  const [unbindOpen, setUnbindOpen] = useState(false);
  const entry = query.data?.entry;

  const releaseAllowed = (query.data?.operations ?? []).some((rule) => rule.operation === "release" && rule.allowed);

  return (
    <>
      <Dialog
        open
        tier="M"
        title={entry?.name ?? path}
        description={t("fileManager.managedDialog.sub")}
        onRequestClose={() => (unbindOpen ? undefined : onClose)}
        footer={
          <>
            <Button onClick={onClose}>{t("ui.close")}</Button>
            {releaseAllowed ? (
              <Button variant="danger" onClick={() => setUnbindOpen(true)}>
                {t("fileManager.managedDialog.unbind")}
              </Button>
            ) : null}
          </>
        }
      >
        {query.error ? (
          <Alert tone="error" title={toUserMessage(query.error, t("fileManager.detail.loadFailed"))} />
        ) : !query.data ? (
          <p className="tw:m-0 tw:text-sm tw:text-muted-foreground">{t("fileManager.detail.loading")}</p>
        ) : (
          <ManagedFileBody detail={query.data} onOpenAnime={onOpenAnime} onOpenRecord={onOpenRecord} />
        )}
      </Dialog>
      {unbindOpen && query.data ? (
        <UnbindDialog
          path={path}
          expectedRevision={query.data.revision}
          onClose={() => setUnbindOpen(false)}
          onDone={onClose}
        />
      ) : null}
    </>
  );
}

function ManagedFileBody({
  detail,
  onOpenAnime,
  onOpenRecord,
}: {
  detail: FileManagerEntryDetail;
  onOpenAnime: (managedAnimeID: string) => void;
  onOpenRecord: (acquisitionID: string) => void;
}) {
  const entry = detail.entry;
  const items: { label: string; value: React.ReactNode }[] = [
    {
      label: t("fileManager.detail.state"),
      value: (
        <span className="tw:inline-flex tw:items-center tw:gap-1">
          <StatePill tone="warning" icon={Lock} busy={entry.state === "in_progress"}>
            {tryT(`fileManager.state.${entry.state}`) ?? entry.state}
          </StatePill>
        </span>
      ),
    },
  ];
  if (entry.kind === "directory") items.push({ label: t("fileManager.detail.kind"), value: t("fileManager.kind.directory") });
  if (entry.role) items.push({ label: t("fileManager.detail.role"), value: tryT(`fileManager.role.${entry.role}`) ?? entry.role });
  if (entry.size_bytes !== undefined) items.push({ label: t("fileManager.detail.size"), value: formatBytes(entry.size_bytes) });
  if (entry.modified_at) items.push({ label: t("fileManager.detail.modified"), value: formatDateTime(entry.modified_at) });
  if (detail.anime) {
    const anime = detail.anime;
    items.push({
      label: t("fileManager.detail.anime"),
      value: (
        <Button size="sm" variant="link" title={t("fileManager.detail.openAnime")} onClick={() => onOpenAnime(anime.managed_anime_id)}>
          {anime.title}
        </Button>
      ),
    });
  }
  if (detail.acquisition_id) {
    const acquisitionID = detail.acquisition_id;
    items.push({
      label: t("fileManager.detail.record"),
      value: (
        <Button size="sm" variant="link" onClick={() => onOpenRecord(acquisitionID)}>
          {t("fileManager.detail.openRecord")}
        </Button>
      ),
    });
  }
  if (detail.in_progress.length > 0) {
    items.push({
      label: t("fileManager.detail.processing"),
      value: detail.in_progress.map((owner, index) => (
        <span key={`${owner.kind}:${owner.acquisition_id ?? index}`}>
          {tryT(`fileManager.inProgressKind.${owner.kind}`) ?? owner.kind}
          {owner.acquisition_id ? " · " : ""}
          {owner.acquisition_id ? (
            <Button size="sm" variant="link" onClick={() => onOpenRecord(owner.acquisition_id!)}>
              {t("fileManager.detail.openRecord")}
            </Button>
          ) : null}
        </span>
      )),
    });
  }
  if (detail.companions.length > 0) {
    items.push({
      label: t("fileManager.detail.companions"),
      value: detail.companions.map((companion, index) => (
        <span key={companion.path ?? index} className="tw:break-all">
          {companion.path ? companion.path.split("/").pop() : tryT(`fileManager.role.${companion.role}`) ?? companion.role}
          {!companion.present ? ` · ${t("fileManager.detail.missing")}` : ""}
        </span>
      )),
    });
  }
  return (
    <div className="tw:grid tw:gap-3">
      <DescriptionList items={items} />
      <p className="tw:m-0 tw:text-xs tw:text-muted-foreground">{t("fileManager.managedDialog.hint")}</p>
    </div>
  );
}

// 解除管理 ends the management where the file stands; the bound subtitles in
// the same folder end with it. It is confirmed once and cannot be undone.
function UnbindDialog({
  path,
  expectedRevision,
  onClose,
  onDone,
}: {
  path: string;
  expectedRevision: string;
  onClose: () => void;
  onDone: () => void;
}) {
  const queryClient = useQueryClient();
  const idempotency = useIdempotencyKeys();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function run() {
    if (busy) return;
    setBusy(true);
    setError(null);
    try {
      await releaseFileManagerEntry({ path, expected_revision: expectedRevision }, idempotency(path));
      void queryClient.invalidateQueries({ queryKey: fileManagerKeys.all });
      onDone();
    } catch (cause) {
      setError(toUserMessage(cause, t("common.operationFailed")));
      setBusy(false);
    }
  }

  return (
    <Dialog
      open
      tier="S"
      blocking
      title={t("fileManager.managedDialog.unbindTitle")}
      onRequestClose={busy ? () => {} : onClose}
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            {t("act.cancel")}
          </Button>
          <Button variant="danger" loading={busy} onClick={() => void run()}>
            {t("fileManager.managedDialog.unbindSubmit")}
          </Button>
        </>
      }
    >
      <div className="tw:grid tw:gap-2">
        <p className="tw:m-0 tw:text-sm">{t("fileManager.managedDialog.unbindLine1")}</p>
        <p className="tw:m-0 tw:text-xs tw:text-muted-foreground">{t("fileManager.managedDialog.unbindLine2")}</p>
        {error ? <Alert tone="error" title={error} /> : null}
      </div>
    </Dialog>
  );
}
