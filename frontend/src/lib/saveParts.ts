import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from "react";

// The single-save orchestration layer (styling-contract S4): one surface (a Tab of the config
// dialog, one settings section) has a single save button, but several existing endpoints may
// sit behind it. Each endpoint's editing area registers here as a part (unsaved-change count +
// name + submit function); the surface's save button submits the changed parts one by one in a
// fixed order, stopping at the first failure — the failed part and everything after it stay
// unsaved, and the failing one is reported. The endpoint split is never shown to the user: one
// button, one success notice.

export type SaveOutcome = { ok: true; message?: string } | { ok: false };

type PartState = { count: number; label: string; busy: boolean };

type Registrar = {
  set: (key: string, state: PartState | null) => void;
  setSaver: (key: string, save: (() => Promise<SaveOutcome>) | null) => void;
};

const SavePartsContext = createContext<Registrar | null>(null);

export const SavePartsProvider = SavePartsContext.Provider;

export type SaveRun = { ok: boolean; messages: string[] };

/** Surface-level orchestration: order is the submission order (it also bounds which keys get submitted). */
export function useSaveParts(order: readonly string[]) {
  const [parts, setParts] = useState<Record<string, PartState>>({});
  const savers = useRef(new Map<string, () => Promise<SaveOutcome>>());
  const [saving, setSaving] = useState(false);
  const [failed, setFailed] = useState<string>();

  const registrar = useMemo<Registrar>(
    () => ({
      set: (key, state) =>
        setParts((prev) => {
          if (state === null) {
            if (!(key in prev)) return prev;
            const next = { ...prev };
            delete next[key];
            return next;
          }
          const current = prev[key];
          if (current && current.count === state.count && current.label === state.label && current.busy === state.busy) return prev;
          return { ...prev, [key]: state };
        }),
      setSaver: (key, save) => {
        if (save) savers.current.set(key, save);
        else savers.current.delete(key);
      },
    }),
    [],
  );

  const count = useCallback(
    (keys?: readonly string[]) =>
      (keys ?? Object.keys(parts)).reduce((sum, key) => sum + (parts[key]?.count ?? 0), 0),
    [parts],
  );
  const busy = saving || Object.values(parts).some((part) => part.busy);

  const total = count();
  // Once the changes are all gone (saved successfully, edits reverted), the previous failure notice no longer holds.
  useEffect(() => {
    if (total === 0) setFailed(undefined);
  }, [total]);

  const save = useCallback(
    async (keys?: readonly string[]): Promise<SaveRun> => {
      const scope = order.filter((key) => (keys === undefined || keys.includes(key)) && (parts[key]?.count ?? 0) > 0);
      setSaving(true);
      setFailed(undefined);
      const messages: string[] = [];
      try {
        for (const key of scope) {
          const submit = savers.current.get(key);
          if (!submit) continue;
          const result = await submit();
          if (!result.ok) {
            setFailed(parts[key]?.label);
            return { ok: false, messages };
          }
          if (result.message) messages.push(result.message);
        }
        return { ok: true, messages };
      } finally {
        setSaving(false);
      }
    },
    [order, parts],
  );

  return { registrar, parts, count, busy, saving, failed, save };
}

/**
 * 登记一个保存 part。没有外层编排（例如组件被单独渲染）时是空操作。
 * save 每次渲染取最新闭包，调用方不必 memo。
 */
export function useSavePart(
  key: string,
  part: { count: number; label: string; busy?: boolean; save: () => Promise<SaveOutcome> },
) {
  const registrar = useContext(SavePartsContext);
  const saveRef = useRef(part.save);
  useEffect(() => {
    saveRef.current = part.save;
  });
  useEffect(() => {
    if (!registrar) return;
    registrar.setSaver(key, () => saveRef.current());
    return () => {
      registrar.setSaver(key, null);
      registrar.set(key, null);
    };
  }, [registrar, key]);
  const busy = part.busy ?? false;
  useEffect(() => {
    registrar?.set(key, { count: part.count, label: part.label, busy });
  }, [registrar, key, part.count, part.label, busy]);
}
