import { createElement } from "react";
import { createRoot } from "react-dom/client";
import { ConfirmDialog } from "../../ui/ConfirmDialog";
import { t } from "../../lib/i18n";

// Imperative confirm for non-React call sites: mounts a ConfirmDialog in its own
// root, resolves once (true = proceed, false = cancel/Esc/overlay) and unmounts after
// the dialog's exit transition. Base UI returns focus to the element that was
// focused when it opened.
export function confirmBusinessReplace(
  content: string,
  labels: { title?: string; confirmLabel?: string } = {},
): Promise<boolean> {
  return new Promise((resolve) => {
    const host = document.createElement("div");
    document.body.appendChild(host);
    const root = createRoot(host);
    let settled = false;
    const finish = (value: boolean) => {
      if (settled) return;
      settled = true;
      resolve(value);
      render(false);
      // Let the exit transition finish before tearing the root down.
      window.setTimeout(() => {
        root.unmount();
        host.remove();
      }, 300);
    };
    const render = (open: boolean) =>
      root.render(
        createElement(ConfirmDialog, {
          open,
          onOpenChange: (next: boolean) => {
            // ConfirmDialog calls onOpenChange(false) before onConfirm(); defer so a
            // confirm wins over the implicit dismissal.
            if (!next) queueMicrotask(() => finish(false));
          },
          title: labels.title ?? t("config.replaceTitle"),
          description: content,
          confirmLabel: labels.confirmLabel ?? t("config.replaceConfirm"),
          cancelLabel: t("act.cancel"),
          onConfirm: () => finish(true),
        }),
      );
    render(true);
  });
}
