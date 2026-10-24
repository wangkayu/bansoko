import { createContext, useContext } from "react";

// Opener of the route-hosted execution records dialog (`?record=…`), provided
// by RecordLinkProvider under the application shell. Outside the shell
// (isolated component tests) there is no opener and the entry points are not
// rendered.

export type RecordLinkTarget = { acquisitionID: string } | { managedAnimeID: string };
export type RecordLinkOpener = (target: RecordLinkTarget) => void;

export const RecordLinkContext = createContext<RecordLinkOpener | undefined>(undefined);

export function useRecordLink(): RecordLinkOpener | undefined {
  return useContext(RecordLinkContext);
}
