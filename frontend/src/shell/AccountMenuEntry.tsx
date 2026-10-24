import type { ComponentProps } from "react";
import { AccountMenu } from "./AccountMenu";

export function AccountMenuEntry(props: ComponentProps<typeof AccountMenu>) {
  return <AccountMenu {...props} />;
}
