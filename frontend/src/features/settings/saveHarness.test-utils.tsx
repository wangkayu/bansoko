import type { ReactNode } from "react";
import { SavePartsProvider } from "../../lib/saveParts";
import { useSettingsSaveParts } from "./settingsSave";
import { SettingsSaveBar } from "./SettingsSaveBar";

// When rendering an in-place-edited section standalone, mirrors the settings dialog's single save: register a part plus the footer save bar.
export function SaveHarness({ children }: { children: ReactNode }) {
  const parts = useSettingsSaveParts();
  return (
    <SavePartsProvider value={parts.registrar}>
      {children}
      <SettingsSaveBar parts={parts} />
    </SavePartsProvider>
  );
}
