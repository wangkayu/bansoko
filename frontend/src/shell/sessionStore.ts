import { create } from "zustand";
import { setCsrfTokenProvider, setSessionPresenceProvider, setUnauthorizedHandler } from "../lib/api/client";

// Session store — the single client-state owner for *whether* a session is
// open, never for the credential itself. The session token lives in an
// HttpOnly cookie the browser attaches on its own, so script (and an injected
// script) cannot read it; only `username` is kept here for display.
//
// `authenticated` is the gate's switch. It is derived from what the server
// said, not from storage: a page load starts unknown until the
// `GET /auth/session` probe answers.

const USERNAME_KEY = "bansoko.username";

export const useSessionStore = create<{
  authenticated: boolean;
  username: string | null;
  setAuthenticated: (username: string) => void;
  setUsername: (username: string) => void;
  clearSession: () => void;
}>((set) => ({
  authenticated: false,
  username: typeof window !== "undefined" ? window.localStorage.getItem(USERNAME_KEY) : null,
  setAuthenticated: (username) => {
    window.localStorage.setItem(USERNAME_KEY, username);
    set({ authenticated: true, username });
  },
  setUsername: (username) => {
    window.localStorage.setItem(USERNAME_KEY, username);
    set({ username });
  },
  clearSession: () => {
    window.localStorage.removeItem(USERNAME_KEY);
    set({ authenticated: false, username: null });
  },
}));

// The client asks these two providers on every request: whether a session is
// believed open (so a 401 can drop it), and the CSRF value to echo. The CSRF
// cookie is deliberately readable by script — that is the whole double-submit
// mechanism — while the session cookie is not.
setSessionPresenceProvider(() => useSessionStore.getState().authenticated);
setCsrfTokenProvider(readCsrfCookie);
setUnauthorizedHandler(() => {
  useSessionStore.getState().clearSession();
});

/** Reads a cookie by name. Returns "" when the cookie is absent or storage is
 *  blocked; the server then refuses the unsafe request and the page reloads. */
function readCsrfCookie(): string {
  if (typeof document === "undefined") return "";
  for (const part of document.cookie.split(";")) {
    const trimmed = part.trim();
    if (!trimmed.startsWith("bansoko_csrf=")) continue;
    return decodeURIComponent(trimmed.slice("bansoko_csrf=".length));
  }
  return "";
}
