import type { ComponentProps } from "react";
import { QBManagerDialog } from "./QBManagerDialog";

export function QBManagerDialogEntry(props: ComponentProps<typeof QBManagerDialog>) {
  return <QBManagerDialog {...props} />;
}
