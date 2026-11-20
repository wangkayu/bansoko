import { Toaster } from "sonner";
import { useTheme } from "next-themes";
import type { ReactElement } from "react";

// Sonner toasts themed via the app's CSS variables so light/dark follow
// next-themes like every other surface. Top-center: a bottom-right toast sat
// over modal dialog footers (save → subscribe), and while a modal is open the
// toast lives in the inert app root, so it swallowed the click on the footer's
// primary action.
export function ThemedToaster({ inCatalogList = false }: { inCatalogList?: boolean } = {}): ReactElement {
  const { resolvedTheme } = useTheme();
  return (
    <Toaster
      // Expose the list's copy while its focus trap is active.
      className={inCatalogList ? "catalog-list-toaster" : "tw:[:root:has(.catalog-list-toaster)_&]:hidden"}
      style={inCatalogList ? { position: "absolute" } : undefined}
      // Keep the list's filters and batch footer clear of actionable notices.
      position={inCatalogList ? "bottom-center" : "top-center"}
      offset={inCatalogList ? { bottom: 80 } : undefined}
      mobileOffset={inCatalogList ? { bottom: 80 } : undefined}
      visibleToasts={inCatalogList ? 1 : undefined}
      theme={resolvedTheme === "dark" ? "dark" : "light"}
      toastOptions={{
        style: {
          background: "hsl(var(--card))",
          border: "1px solid hsl(var(--border))",
          color: "hsl(var(--foreground))",
        },
      }}
    />
  );
}
