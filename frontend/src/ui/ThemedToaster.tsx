import { Toaster } from "sonner";
import { useTheme } from "next-themes";
import type { ReactElement } from "react";

// Sonner toasts themed via the app's CSS variables so light/dark follow
// next-themes like every other surface. Top-center: a bottom-right toast sat
// over modal dialog footers (save → subscribe), and while a modal is open the
// toast lives in the inert app root, so it swallowed the click on the footer's
// primary action.
export function ThemedToaster({
  inCatalogList = false,
  inDialog = false,
}: {
  inCatalogList?: boolean;
  inDialog?: boolean;
} = {}): ReactElement {
  const { resolvedTheme } = useTheme();
  const scoped = inCatalogList || inDialog;
  const className = inCatalogList
    ? "catalog-list-toaster"
    : inDialog
      ? "dialog-toaster"
      : "tw:[:root:has(.catalog-list-toaster,.dialog-toaster)_&]:hidden";
  return (
    <Toaster
      // Expose actionable toasts inside a modal's focus trap while hiding the inert root toaster.
      className={className}
      style={scoped ? { position: "absolute" } : undefined}
      // Keep the list's filters and batch footer clear of actionable notices.
      position={inCatalogList ? "bottom-center" : "top-center"}
      offset={inCatalogList ? { bottom: 80 } : inDialog ? { top: 12 } : undefined}
      mobileOffset={inCatalogList ? { bottom: 80 } : inDialog ? { top: 12 } : undefined}
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
