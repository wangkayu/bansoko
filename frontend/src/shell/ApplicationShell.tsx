import { Outlet } from "react-router-dom";
import { TopNav } from "./TopNav";
import { RecordLinkProvider } from "./recordLink";
import { useSessionStore } from "./sessionStore";

// Application shell: top navigation + a single scrollable canvas.
// Routes render below; there is no cross-anime task center, activity surface
// or diagnostics entry in the product.

export function ApplicationShell({ onLogout }: { onLogout: () => void }) {
  const username = useSessionStore((s) => s.username);
  return (
    <RecordLinkProvider>
      <div className="app-shell">
        <TopNav username={username} onLogout={onLogout} />
        <main className="app-main">
          <div className="app-content">
            <Outlet />
          </div>
        </main>
      </div>
    </RecordLinkProvider>
  );
}
