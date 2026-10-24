import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createHashRouter, RouterProvider } from "react-router-dom";
import { ThemeProvider } from "next-themes";
import { AppRoutes } from "./shell/AppRoutes";
import { ThemedToaster } from "./ui/ThemedToaster";
import { ErrorBoundary } from "./ui/ErrorBoundary";
import { initRealtimeInvalidation } from "./lib/events/useRealtimeInvalidation";
import { installDialogFocusReturn } from "./ui/base/focusReturn";
import { LocaleScope } from "./ui/LocaleScope";

// Bootstrap gate → Application Shell with a catalog canvas and routed
// context dialogs. `/subscriptions` and `/naming` are retired redirects; routed
// settings and context dialogs render as overlays while the catalog stays mounted.

const queryClient = new QueryClient({
  defaultOptions: {
    queries: { staleTime: 15_000, retry: 1, refetchOnWindowFocus: false },
  },
});

const router = createHashRouter([
  {
    path: "*",
    element: <AppRoutes />,
  },
]);

// One app-wide SSE connection pushing read-model invalidation signals.
// Starts once the session store holds a token; manual refresh stays fallback.
initRealtimeInvalidation(queryClient);

// Dialogs return focus to their trigger even when a tap/click did not focus it
// (touch, WebKit); the listener must exist before the first lazily loaded dialog.
installDialogFocusReturn();

const App = () => {
  return (
    <ErrorBoundary>
      {/* next-themes toggles the light/dark class on <html> and the
          :root / .dark variable blocks in index.css re-theme everything at once;
          storageKey "theme" matches the no-flash script in index.html. */}
      <ThemeProvider attribute="class" defaultTheme="light" enableSystem themes={["light", "dark"]} storageKey="theme">
        <QueryClientProvider client={queryClient}>
          <LocaleScope>
            <ThemedToaster />
            <RouterProvider router={router} />
          </LocaleScope>
        </QueryClientProvider>
      </ThemeProvider>
    </ErrorBoundary>
  );
};

export default App;