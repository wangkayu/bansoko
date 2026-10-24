import type { ComponentProps } from "react";
import { AddCandidateDialog } from "./AddCandidateDialog";

export function AddCandidateDialogEntry(props: ComponentProps<typeof AddCandidateDialog>) {
  return <AddCandidateDialog {...props} />;
}
