import { useCallback, useState } from "react";

// The segment switch's choice (subscriptions / one-shot / other): a link parameter wins; with
// no parameter, this browser's last choice; failing that, the first option. A mere per-browser
// convenience memory: when localStorage is unusable (private mode, cleared, test environments)
// reads and writes skip silently and the page keeps working.

function readStored<T extends string>(key: string, values: readonly T[]): T | undefined {
  try {
    const stored = globalThis.localStorage?.getItem(key);
    return values.find((value) => value === stored);
  } catch {
    return undefined;
  }
}

function writeStored(key: string, value: string) {
  try {
    globalThis.localStorage?.setItem(key, value);
  } catch {
    // When unwritable, nothing is remembered and the current choice is unaffected.
  }
}

/**
 * 当前分段：`routeValue`（链接参数）存在时就是它；否则是本浏览器上次的选择或第一个选项。
 * 切换时记下选择并交给 `onRouteChange` 写回链接。
 */
export function useRememberedSegment<T extends string>({
  storageKey,
  values,
  routeValue,
  onRouteChange,
}: {
  storageKey: string;
  values: readonly T[];
  routeValue?: T;
  onRouteChange?: (value: T) => void;
}): [T, (value: T) => void] {
  const [local, setLocal] = useState<T>(() => readStored(storageKey, values) ?? values[0]);
  const current = routeValue ?? local;
  const change = useCallback(
    (value: T) => {
      setLocal(value);
      writeStored(storageKey, value);
      onRouteChange?.(value);
    },
    [onRouteChange, storageKey],
  );
  return [current, change];
}
