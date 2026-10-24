import type { ComponentProps } from "react";
import { ExecutionRecordsDialog } from "./ExecutionRecordsDialog";

export function ExecutionRecordsDialogEntry(props: ComponentProps<typeof ExecutionRecordsDialog>) {
  return <ExecutionRecordsDialog {...props} />;
}
