import type { ComponentProps } from "react";
import { OneShotDialog } from "./OneShotDialog";

export function OneShotDialogEntry(props: ComponentProps<typeof OneShotDialog>) {
  return <OneShotDialog {...props} />;
}
