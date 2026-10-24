import { useCallback, useEffect, useState } from "react";

// Keeps owner edits alive: when saved updates from a workspace re-read, local edits unequal to
// saved are kept (user input is never clobbered); equal or not-yet-edited follows saved.
//
// markSaved: between a successful save and the re-read's return the server baseline is still the
// old value; the just-saved value is recorded as the baseline first, so the gap never misreports
// unsaved changes. Once saved changes (the re-read returns), the server baseline takes over.
export function useOwnerEdit<T>(saved: T | undefined, isEqual: (a: T, b: T) => boolean) {
  const [edit, setEdit] = useState<T | undefined>(undefined);
  const [committed, setCommitted] = useState<{ base: T | undefined; value: T }>();
  useEffect(() => {
    setCommitted((current) => (current && current.base !== saved ? undefined : current));
    if (saved === undefined) return;
    setEdit((current) => (current !== undefined && !isEqual(saved, current) ? current : saved));
  }, [saved, isEqual]);
  const baseline = committed && committed.base === saved ? committed.value : saved;
  const dirty = edit !== undefined && baseline !== undefined && !isEqual(baseline, edit);
  const markSaved = useCallback(
    (value: T) => {
      setCommitted({ base: saved, value });
      setEdit(value);
    },
    [saved],
  );
  return { edit: edit ?? saved, setEdit, dirty, baseline, markSaved };
}
