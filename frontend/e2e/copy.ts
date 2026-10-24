import { setLocale, t } from "../src/lib/i18n";
import { joinList } from "../src/lib/i18n/format";

// Specs assert UI copy through the app's own catalog instead of hard-coded
// strings. playwright.config.ts pins the browser locale to zh-CN; the Node side
// would otherwise detect the runner's language, so pin the same locale here
// (without touching storage).
setLocale("zh-CN", { persist: false });

export { joinList, t };
