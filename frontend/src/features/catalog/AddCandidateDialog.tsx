import { useState } from "react";
import { toast } from "../../ui/toast";
import type { AnimeDraft } from "../../lib/api/client";
import { getCandidateStore } from "../../lib/candidate/serverCandidateStore";
import { toUserMessage } from "../../lib/api/client";
import { Button, Input } from "../../ui";
import { Dialog } from "../../ui/base";
import { withGlobalBusiness } from "../anime-config/businessDraft";
import { t } from "../../lib/i18n";
import { FeedSiteNote } from "./FeedSiteNote";

/** Adds a custom candidate: saves the full AnimeDraft into the candidate store, calling no subscription endpoint. */
export function AddCandidateDialog({
  open,
  onOpenChange,
  onAdded,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onAdded: () => void;
}) {
  const [title, setTitle] = useState("");
  const [instanceLabel, setInstanceLabel] = useState("");
  const [rssUrl, setRssUrl] = useState("");
  const [groupName, setGroupName] = useState("");
  const [saving, setSaving] = useState(false);

  const valid =
    title.trim() !== "" &&
    instanceLabel.trim() !== "" &&
    groupName.trim() !== "" &&
    rssUrl.trim() !== "";

  async function submit() {
    if (!valid) return;
    setSaving(true);
    try {
      const draft: AnimeDraft = {
        origin: "custom",
        origin_entry_ref: null,
        title: title.trim(),
        title_override: null,
        instance_label: instanceLabel.trim(),
        metadata: { bgm: null, tmdb: null },
        subgroups: { max_rss: 3, entries: [] },
        // withGlobalBusiness copies the default settings into each rules section.
        rules: { sections: {} },
        rss_sources: [
          {
            source_kind: "custom_rss",
            locator: rssUrl.trim(),
            locator_display: rssUrl.trim(),
            group_name: groupName.trim(),
            title: title.trim(),
            episode_offset: 0,
          },
        ],
      };
      const store = getCandidateStore();
      const saved = await store.save(await withGlobalBusiness(draft));
      // origin_entry_ref is already null in the draft (custom never self-references), so no second
//       PATCH after saving is needed to write the id back.
      void saved;
      setTitle("");
      setInstanceLabel("");
      setRssUrl("");
      setGroupName("");
      onAdded();
    } catch (cause) {
      toast.error(toUserMessage(cause, t("addPreset.saveFailed")));
    } finally {
      setSaving(false);
    }
  }

  return (
    <Dialog
      open={open}
      onRequestClose={() => onOpenChange(false)}
      tier="S"
      title={t("addPreset.title")}
      footer={
        <>
          <Button onClick={() => onOpenChange(false)}>{t("act.cancel")}</Button>
          <Button variant="primary" disabled={!valid || saving} onClick={() => void submit()}>
            {saving ? t("act.saving") : t("addPreset.save")}
          </Button>
        </>
      }
    >
      <div className="candidate-form">
        <label className="candidate-field">
          {t("addPreset.fieldTitle")}
          <Input value={title} onChange={(e) => setTitle(e.target.value)} />
        </label>
        <label className="candidate-field">
          {t("addPreset.fieldLabel")}
          <Input value={instanceLabel} onChange={(e) => setInstanceLabel(e.target.value)} placeholder={t("addPreset.labelPlaceholder")} />
        </label>
        <label className="candidate-field">
          {t("addPreset.fieldRss")}
          <Input
            value={rssUrl}
            onChange={(e) => setRssUrl(e.target.value)}
            placeholder="https://example.com/feed.xml"
          />
          <FeedSiteNote url={rssUrl} />
        </label>
        <label className="candidate-field">
          {t("addPreset.fieldGroup")}
          <Input value={groupName} onChange={(e) => setGroupName(e.target.value)} />
        </label>
        <p className="candidate-note">
          {t("addPreset.note")}
        </p>
      </div>
    </Dialog>
  );
}
