import { useState } from "react";
import { Button } from "../../ui";
import { toUserMessage } from "../../lib/api/client";
import { t } from "../../lib/i18n";

export type CustomSnapshotIdentity = { snapshot_id: string; snapshot_version: number; content_hash: string };
export type CustomSnapshotRequest = { candidate_id: string; revision: number; expected_version: number };

/** Parent wires the generated explicit-fetch API. Rendering never invokes it. */
export function CustomSnapshotFetchButton({ request, fetchSnapshot, onSnapshot }: {
 request: CustomSnapshotRequest;
 fetchSnapshot: (request: CustomSnapshotRequest) => Promise<CustomSnapshotIdentity>;
 onSnapshot: (identity: CustomSnapshotIdentity) => void;
}) {
 const [busy, setBusy] = useState(false);
 const [error, setError] = useState<string | null>(null);
 async function fetchExplicitly() {
  setBusy(true); setError(null);
  try { onSnapshot(await fetchSnapshot(request)); }
  catch (cause) { setError(toUserMessage(cause, t("addPreset.fetchFailed"))); }
  finally { setBusy(false); }
 }
 return <div><Button disabled={busy} onClick={() => void fetchExplicitly()}>{busy ? t("addPreset.fetching") : t("addPreset.fetch")}</Button>{error && <p role="alert">{error}</p>}</div>;
}
