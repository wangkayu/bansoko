import { Toaster } from "sonner";
import { useTheme } from "next-themes";
import type { ReactElement } from "react";

// Sonner toasts themed via the app's CSS variables so light/dark follow
// next-themes like every other surface. Top-center: a bottom-right toast sat
// over modal dialog footers (save → subscribe), and while a modal is open the
// toast lives in the inert app root, so it swallowed the click on the footer's
// primary action.
export function ThemedToaster(): ReactElement {
  const { resolvedTheme } = useTheme();
  return (
    <Toaster
      position="top-center"
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
