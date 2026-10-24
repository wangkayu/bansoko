import type { ComponentProps } from "react";
import { FileManagerDialog } from "./FileManagerDialog";

export function FileManagerDialogEntry(props: ComponentProps<typeof FileManagerDialog>) {
  return <FileManagerDialog {...props} />;
}
