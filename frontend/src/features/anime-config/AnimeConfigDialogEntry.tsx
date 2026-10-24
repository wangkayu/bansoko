import type { ComponentProps } from "react";
import { AnimeConfigDialog } from "./AnimeConfigDialog";

export function AnimeConfigDialogEntry(props: ComponentProps<typeof AnimeConfigDialog>) {
  return <AnimeConfigDialog {...props} />;
}
