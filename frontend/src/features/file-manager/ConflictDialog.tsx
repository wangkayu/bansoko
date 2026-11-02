import { useState } from "react";
import { Alert, Button, Dialog } from "../../ui/base";
import { t } from "../../lib/i18n";

// ConflictDialog — the target-busy choice the browse layer offers when its
// pre-submit check finds occupied targets: skip the conflicting files, give
// each a version suffix, replace the existing file, or cancel (nothing is
// changed). Replacing is offered only when every listed target is a plain file
// on disk; a file a subscription manages or an operation holds is never
// replaced. The choice is the loudest in the dialog: a warning says what it
// destroys and the button carries the danger colour.
const SHOWN = 6;

export function ConflictDialog({
  targets,
  busy,
  onCancel,
  onSkip,
  onVersion,
  onOverwrite,
}: {
  /** Target names (or full paths) the check found occupied. */
  targets: string[];
  busy?: boolean;
  onCancel: () => void;
  onSkip: () => void;
  onVersion: () => void;
  /** Replaces the existing files. Absent when any target is managed or in progress. */
  onOverwrite?: () => void;
}) {
  const [chosen, setChosen] = useState<"skip" | "version" | "overwrite" | null>(null);
  const shown = targets.slice(0, SHOWN);
  const more = targets.length - shown.length;
  return (
    <Dialog
      open
      blocking
      tier="S"
      title={t("fileManager.conflictChoice.title")}
      description={t("fileManager.conflictChoice.hint")}
      onRequestClose={busy ? () => {} : onCancel}
      footer={
        <>
          {/* Cancel leaves the group: it sits on the left, away from the actions. */}
          <Button size="sm" className="tw:mr-auto" onClick={onCancel} disabled={busy}>
            {t("fileManager.conflictChoice.cancel")}
          </Button>
          <Button
            size="sm"
            onClick={() => {
              setChosen("skip");
              onSkip();
            }}
            loading={busy && chosen === "skip"}
            disabled={busy && chosen !== "skip"}
          >
            {t("fileManager.conflictChoice.skip")}
          </Button>
          <Button
            size="sm"
            onClick={() => {
              setChosen("version");
              onVersion();
            }}
            loading={busy && chosen === "version"}
            disabled={busy && chosen !== "version"}
          >
            {t("fileManager.conflictChoice.version")}
          </Button>
          {onOverwrite ? (
            <Button
              size="sm"
              variant="danger"
              onClick={() => {
                setChosen("overwrite");
                onOverwrite();
              }}
              loading={busy && chosen === "overwrite"}
              disabled={busy && chosen !== "overwrite"}
            >
              {t("fileManager.conflictChoice.overwrite")}
            </Button>
          ) : null}
        </>
      }
    >
      <ul className="tw:m-0 tw:grid tw:list-disc tw:gap-1 tw:pl-5 tw:text-sm">
        {shown.map((target) => (
          <li key={target} className="tw:break-all">
            {target}
          </li>
        ))}
        {more > 0 ? <li className="tw:text-muted-foreground">{t("fileManager.conflictChoice.more", { n: more })}</li> : null}
      </ul>
      <p className="tw:m-0 tw:mt-3 tw:text-xs tw:text-muted-foreground">
        {t("fileManager.conflictChoice.count", { n: targets.length })}
      </p>
      {onOverwrite ? (
        <div className="tw:mt-3">
          <Alert tone="warning" title={t("fileManager.conflictChoice.overwriteWarning")} />
        </div>
      ) : null}
    </Dialog>
  );
}
