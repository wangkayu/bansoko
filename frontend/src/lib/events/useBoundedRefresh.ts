import { useEffect, useRef } from "react";
import { useQueryClient, type QueryKey } from "@tanstack/react-query";
import { useRealtimeConnected } from "@/lib/events/useRealtimeInvalidation";

// Bounded low-frequency polling fallback for surfaces that rely on push
// invalidation (R5): while the SSE stream is connected nothing is polled;
// once disconnected, refetch at 1s -> 2s -> 4s -> 8s -> 15s (capped). After a
// reconnect the fallback stops and one authoritative refetch closes the gap.
// Only read queries are refetched — this hook never replays writes.
export function useBoundedRefresh(queryKeys: readonly QueryKey[]): void {
  const queryClient = useQueryClient();
  const connected = useRealtimeConnected();
  const keysRef = useRef(queryKeys);
  keysRef.current = queryKeys;

  useEffect(() => {
    if (connected) {
      return; // push invalidation is authoritative while connected
    }
    let delay = 1000;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let stopped = false;
    const refetch = () => {
      if (stopped || document.hidden) return;
      for (const key of keysRef.current) {
        void queryClient.refetchQueries({ queryKey: key, type: "active" }, { cancelRefetch: false });
      }
      delay = Math.min(delay * 2, 15000);
      timer = setTimeout(refetch, delay);
    };
    const resume = () => {
      if (timer) clearTimeout(timer);
      delay = 1000;
      // SSE deliberately disconnects in hidden tabs. Do not turn that pause
      // into background polling; give its reconnect the first chance to read.
      if (!document.hidden) timer = setTimeout(refetch, delay);
    };
    resume();
    document.addEventListener("visibilitychange", resume);
    return () => {
      stopped = true;
      if (timer) clearTimeout(timer);
      document.removeEventListener("visibilitychange", resume);
    };
  }, [connected, queryClient]);

  const wasDisconnected = useRef(false);
  useEffect(() => {
    if (connected && wasDisconnected.current) {
      // Reconnect: authoritative read to close the event gap, then the
      // connected effect above stops the fallback.
      for (const key of keysRef.current) {
        void queryClient.refetchQueries({ queryKey: key, type: "active" });
      }
    }
    wasDisconnected.current = !connected;
  }, [connected, queryClient]);
}
