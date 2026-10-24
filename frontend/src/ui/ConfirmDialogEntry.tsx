import type { ComponentProps } from "react";
import { ConfirmDialog } from "./ConfirmDialog";

// Stable import surface for confirm dialogs.
export function ConfirmDialogEntry(props: ComponentProps<typeof ConfirmDialog>) {
  return <ConfirmDialog {...props} />;
}
