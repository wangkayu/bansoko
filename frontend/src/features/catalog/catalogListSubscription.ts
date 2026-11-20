import { ApiError, newUUID } from "../../lib/api/client";
import { CandidateWorkingDuplicate, type AnimeConfigCandidate, type CandidateStore } from "../../lib/candidate/port";
import { validateDraft } from "../../lib/draft";
import { t } from "../../lib/i18n";
import type { CatalogEntryView } from "../../lib/providers/viewmodels";
import { freezeBusinessDraft, withGlobalBusiness } from "../anime-config/businessDraft";
import { draftFromEntry } from "./entryDraft";

export function createCatalogListSubscriber(store: CandidateStore) {
  const attempts = new Map<string, { candidate: AnimeConfigCandidate; key: string }>();

  async function candidateFor(entry: CatalogEntryView): Promise<AnimeConfigCandidate | null> {
    if (entry.source === "custom") {
      const candidate = entry.candidateId ? await store.get(entry.candidateId) : null;
      return candidate?.origin === "custom" ? candidate : null;
    }
    const query = { origin: entry.source, origin_entry_ref: entry.providerEntryRef };
    const findWorking = async () => (await store.list(query)).find((candidate) =>
      candidate.origin === query.origin && candidate.origin_entry_ref === query.origin_entry_ref && candidate.status === "working");
    const existing = await findWorking();
    if (existing) return existing;
    const draft = await withGlobalBusiness(draftFromEntry(entry));
    try {
      return await store.save(await freezeBusinessDraft(draft));
    } catch (cause) {
      if (cause instanceof CandidateWorkingDuplicate) return (await findWorking()) ?? null;
      throw cause;
    }
  }

  return async (entry: CatalogEntryView, start: boolean) => {
    const intent = JSON.stringify([entry.source, entry.providerEntryRef, start]);
    const previous = attempts.get(intent);
    const candidate = previous?.candidate ?? await candidateFor(entry);
    if (!candidate || candidate.status !== "working" || !candidate.current_revision) {
      throw new Error(t("config.draftNotFound"));
    }
    const [problem] = validateDraft(candidate.draft);
    if (problem) throw new Error(problem.message);

    // An interrupted response can hide a committed activation. Replay its exact request.
    const key = previous?.key ?? newUUID();
    attempts.set(intent, { candidate, key });
    try {
      const result = await store.subscribe(candidate.id, candidate.version, key, { revision: candidate.current_revision, start });
      attempts.delete(intent);
      return result;
    } catch (cause) {
      if (!(cause instanceof ApiError) || (cause.status > 0 && cause.status < 500)) attempts.delete(intent);
      throw cause;
    }
  };
}
