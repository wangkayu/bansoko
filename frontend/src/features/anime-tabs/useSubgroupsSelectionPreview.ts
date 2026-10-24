import { useEffect, useRef, useState } from "react";
import { previewAnimeSubgroupsSelection, type SubgroupsSelectionPreviewInput } from "../../lib/api/client";
import type { ServerSelectionFact } from "../anime-forms/SourcesFields";
import { t } from "../../lib/i18n";

/** How long subgroup editing must pause before the selection preview is requested (no request per switch while toggling policies). */
export const SELECTION_PREVIEW_DEBOUNCE_MS = 300;

export type SelectionPreviewState = {
  /** The latest successful server selection facts; undefined until a result succeeds. */
  facts?: ServerSelectionFact[];
  /** A preview is waiting or in flight (the displayed numbers are still the previous result). */
  pending: boolean;
  /** A short note of the latest preview's failure; the numbers keep the previous result. */
  error?: string;
};

// The subgroups tab's unsaved-selection preview: "taking effect on save, but never blocking the
// preview". Selection is a server decision (POST /anime/subgroups-selection-preview); the
// frontend only debounces, sends the request and keeps the previous result: while in flight or
// failed, the previous numbers keep showing, and the client never computes a selection.
//
// `key` is the identity of the inputs that decide the selection (only a change re-requests); undefined means no preview is needed and the state clears.
// `input` is read fresh when the debounce expires, so changes outside the key never trigger a request.
export function useSubgroupsSelectionPreview(
  input: SubgroupsSelectionPreviewInput | undefined,
  key: string | undefined,
): SelectionPreviewState {
  const [state, setState] = useState<SelectionPreviewState>({ pending: false });
  const inputRef = useRef(input);
  useEffect(() => {
    inputRef.current = input;
  });
  const sequence = useRef(0);

  useEffect(() => {
    const id = ++sequence.current;
    if (key === undefined) {
      setState({ pending: false });
      return;
    }
    setState((current) => ({ facts: current.facts, pending: true }));
    const timer = setTimeout(() => {
      const request = inputRef.current;
      if (!request) return;
      previewAnimeSubgroupsSelection(request).then(
        (result) => {
          if (sequence.current !== id) return;
          setState({ facts: result.groups, pending: false });
        },
        () => {
          if (sequence.current !== id) return;
          setState((current) => ({ facts: current.facts, pending: false, error: t("forms.sources.selectionFailed") }));
        },
      );
    }, SELECTION_PREVIEW_DEBOUNCE_MS);
    return () => clearTimeout(timer);
  }, [key]);

  return state;
}
