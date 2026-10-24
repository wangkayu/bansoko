import type { ComponentProps } from "react";
import { LogDialog } from "./LogDialog";

export function LogDialogEntry(props: ComponentProps<typeof LogDialog>) {
  return <LogDialog {...props} />;
}
