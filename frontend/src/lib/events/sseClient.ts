// Realtime invalidation SSE client (G1).
//
// The backend stream (`GET /api/v1/events/stream`) carries only lightweight
// read-model invalidation signals — never business objects. EventSource cannot
// send the Authorization header, so this client parses SSE manually over
// fetch + ReadableStream: `data:` lines framed by blank lines, comment lines
// (`:` heartbeats) ignored, each data payload JSON-decoded.
//
// Resilience contract (AutoBangumi blueprint):
// - Exponential backoff reconnect: 1s, doubling, capped at 15s; the backoff
//   resets after any received chunk (data or heartbeat proves liveness).
// - Page hidden (document.visibilitychange) pauses the stream entirely —
//   disconnect and stop reconnecting; becoming visible reconnects.
// - Consumers read `connected` to decide whether to fall back to polling.
// - The session credential is an HttpOnly cookie, so the stream sends no
//   Authorization header: the browser attaches the cookie on this same-origin
//   fetch. Before a session is open (and in development skip-auth, which needs
//   no credential at all) the caller decides via `isAuthenticated`.
// - 401 is treated as a terminal auth failure and reported via onUnauthorized,
//   not retried.

export type RealtimeTarget = "catalog" | "subscribed" | "rss" | "records";

export interface RealtimeEvent {
  target: RealtimeTarget;
  /** Optional scope hint: present only when one anime's owner chain changed. */
  managedAnimeId?: string;
}

const REALTIME_TARGETS: ReadonlySet<string> = new Set(["catalog", "subscribed", "rss", "records"]);

const STREAM_PATH = "/api/v1/events/stream";
const INITIAL_BACKOFF_MS = 1_000;
const MAX_BACKOFF_MS = 15_000;

export interface RealtimeStreamOptions {
  /** Whether a session is believed open. False holds the stream off until the
   *  gate signs in; the credential itself is a cookie the browser attaches. */
  isAuthenticated: () => boolean;
  onEvent: (event: RealtimeEvent) => void;
  onStateChange?: (connected: boolean) => void;
  /** Terminal 401: the stream stops; the app should drop the dead session. */
  onUnauthorized?: () => void;
}

export interface RealtimeStreamHandle {
  start: () => void;
  stop: () => void;
}

export function createRealtimeEventStream(options: RealtimeStreamOptions): RealtimeStreamHandle {
  const { isAuthenticated, onEvent, onStateChange, onUnauthorized } = options;

  let running = false;
  let abort: AbortController | undefined;
  let reconnectTimer: ReturnType<typeof setTimeout> | undefined;
  let backoff = INITIAL_BACKOFF_MS;
  let connected = false;

  function setConnected(next: boolean) {
    if (connected === next) return;
    connected = next;
    onStateChange?.(next);
  }

  function clearReconnect() {
    if (reconnectTimer !== undefined) {
      clearTimeout(reconnectTimer);
      reconnectTimer = undefined;
    }
  }

  function closeConnection() {
    abort?.abort();
    abort = undefined;
    setConnected(false);
  }

  function scheduleReconnect() {
    if (!running || reconnectTimer !== undefined) return;
    const delay = backoff;
    backoff = Math.min(backoff * 2, MAX_BACKOFF_MS);
    reconnectTimer = setTimeout(() => {
      reconnectTimer = undefined;
      void connect();
    }, delay);
  }

  function parseFrame(frame: string) {
    // SSE framing: lines within one frame; blank line terminates the frame.
    // Comment lines start with ":"; only `data:` lines carry payloads.
    // Multiple data lines join with "\n" per the SSE spec.
    const dataLines: string[] = [];
    for (const line of frame.split("\n")) {
      if (line.startsWith(":")) continue;
      if (line.startsWith("data:")) {
        dataLines.push(line.slice(5).replace(/^ /, ""));
      }
    }
    if (dataLines.length === 0) return;
    try {
      const payload = JSON.parse(dataLines.join("\n")) as { target?: unknown; managed_anime_id?: unknown };
      if (typeof payload.target === "string" && REALTIME_TARGETS.has(payload.target)) {
        const event: RealtimeEvent = { target: payload.target as RealtimeTarget };
        if (typeof payload.managed_anime_id === "string" && payload.managed_anime_id !== "") {
          event.managedAnimeId = payload.managed_anime_id;
        }
        onEvent(event);
      }
    } catch {
      // A malformed payload is a server bug, not a connection problem:
      // drop the frame without disturbing the stream or the backoff state.
    }
  }

  async function connect() {
    if (!running || abort) return;
    const controller = new AbortController();
    abort = controller;
    try {
      // No session yet: the app may be mid-login or a fresh page load that has
      // not finished its probe. Connecting unauthenticated would get a 401 and
      // drop a perfectly good session, so wait for the next retry instead. The
      // caller (useRealtimeInvalidation) restarts on login.
      if (!isAuthenticated()) {
        setConnected(false);
        scheduleReconnect();
        return;
      }
      const response = await fetch(STREAM_PATH, {
        credentials: "same-origin",
        signal: controller.signal,
      });
      if (response.status === 401) {
        // Dead session: retrying cannot help; let the app return to login.
        running = false;
        closeConnection();
        onUnauthorized?.();
        return;
      }
      if (!response.ok || !response.body) {
        throw new Error(`event stream failed with status ${response.status}`);
      }
      setConnected(true);
      const reader = response.body.getReader();
      const decoder = new TextDecoder();
      let buffer = "";
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        // Any received chunk proves liveness: reset the backoff.
        backoff = INITIAL_BACKOFF_MS;
        buffer += decoder.decode(value, { stream: true });
        let boundary = buffer.indexOf("\n\n");
        while (boundary !== -1) {
          parseFrame(buffer.slice(0, boundary));
          buffer = buffer.slice(boundary + 2);
          boundary = buffer.indexOf("\n\n");
        }
      }
      // Clean EOF: the server closed the stream; reconnect.
    } catch (cause) {
      if (controller.signal.aborted) return;
      void cause; // Network/parse failures share the reconnect path below.
    } finally {
      if (abort === controller) abort = undefined;
    }
    setConnected(false);
    scheduleReconnect();
  }

  function handleVisibility() {
    if (typeof document === "undefined") return;
    if (document.hidden) {
      clearReconnect();
      closeConnection();
    } else if (running) {
      // Becoming visible again reconnects immediately with a fresh backoff —
      // including the first connect deferred by starting while hidden.
      backoff = INITIAL_BACKOFF_MS;
      clearReconnect();
      closeConnection();
      void connect();
    }
  }

  return {
    start() {
      if (running) return;
      running = true;
      if (typeof document !== "undefined") {
        document.addEventListener("visibilitychange", handleVisibility);
        if (document.hidden) return; // Defer the first connect until visible.
      }
      void connect();
    },
    stop() {
      if (!running) return;
      running = false;
      if (typeof document !== "undefined") {
        document.removeEventListener("visibilitychange", handleVisibility);
      }
      clearReconnect();
      closeConnection();
    },
  };
}
