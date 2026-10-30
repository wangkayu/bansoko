import { useTheme } from "next-themes";
import {
  ArrowUpRight,
  CalendarDays,
  ClipboardList,
  History,
  LogOut,
  Search,
  Settings,
} from "lucide-react";
import type { ReactNode } from "react";
import type { AutomationKind, Connection } from "../lib/api/client";
import { useNextAutomationRun } from "../lib/automationSchedules";
import { formatDateTime } from "../lib/labels";
import { badgeText } from "./logSeen";
import { cn } from "@/lib/utils";
import { Dialog, Segmented, StatePill } from "../ui/base";
import { isLocale, LOCALE_NAMES, LOCALES, setLocale, t, useLocale } from "../lib/i18n";

// The phone "More" bottom sheet: tool entries (with ↗, opening sheets/dialogs;
// hidden anime live in Settings, not here), system status (the downloader is
// one row; with no usable downloader it says "no downloader configured" and
// carries a go-to-settings action), settings, appearance, language and sign
// out fold in here; the desktop top bar has none of it. It is a navigation
// menu, not a route-hosted dialog — the open state is component state and the
// address does not change when it closes.

const AUTOMATION_KIND_LABEL = (): Record<AutomationKind, string> => ({
  group_discovery: t("nav.automationKind.group_discovery"),
  rss_refresh: t("nav.automationKind.rss_refresh"),
  metadata_match: t("nav.automationKind.metadata_match"),
  catalog_refresh: t("nav.automationKind.catalog_refresh"),
});

const CONNECTION_LABEL = (): Record<Connection["kind"], string> => ({
  qbittorrent: t("nav.connection.qbittorrent"),
  rss_http: t("nav.connection.rss_http"),
  tmdb: t("nav.connection.tmdb"),
  jackett: t("nav.connection.jackett"),
  anibt: t("nav.connection.anibt"),
  mikan: t("nav.connection.mikan"),
  nyaa: t("nav.connection.nyaa"),
  bgm: t("nav.connection.bgm"),
});

type MoreSheetProps = {
  open: boolean;
  onClose: () => void;
  onLogout: () => void;
  connections: Connection[];
  failedCount: number;
  onOpenSearch: () => void;
  onOpenRecords: () => void;
  onOpenLog: () => void;
  onOpenSettings: () => void;
  onOpenDownloaderSettings: () => void;
};

// The downloader leads the system section.
// RSS access is only the custom RSS network setting and does not enter the status.
const STATUS_ORDER: Connection["kind"][] = ["qbittorrent", "anibt", "mikan", "nyaa", "bgm", "tmdb", "jackett"];

export function MoreSheet({
  open,
  onClose,
  onLogout,
  connections,
  failedCount,
  onOpenSearch,
  onOpenRecords,
  onOpenLog,
  onOpenSettings,
  onOpenDownloaderSettings,
}: MoreSheetProps) {
  const { theme, setTheme } = useTheme();
  const locale = useLocale();
  const nextRun = useNextAutomationRun(open);

  const noDownloader = !connections.some((connection) => connection.kind === "qbittorrent" && connection.enabled);
  const statusConnections = connections.filter((connection) => STATUS_ORDER.includes(connection.kind)).sort((a, b) => STATUS_ORDER.indexOf(a.kind) - STATUS_ORDER.indexOf(b.kind));

  const launch = (action: () => void) => () => {
    onClose();
    action();
  };
  return (
    <Dialog open={open} onRequestClose={onClose} tier="S" title={t("nav.more")}>
      <div className="tw:grid tw:gap-0.5">
        <p className="more-sheet-section">{t("nav.tools")}</p>
        <MoreRow icon={<Search width={18} height={18} />} label={t("nav.search")} onSelect={launch(onOpenSearch)} />
        {/* The file-manager row is hidden in this version; on restore bring back onOpenFiles and this row, see the note at the top of TopNav.tsx. */}
        <MoreRow icon={<History width={18} height={18} />} label={t("nav.records")} onSelect={launch(onOpenRecords)} />
        <MoreRow
          icon={<ClipboardList width={18} height={18} />}
          label={t("nav.log")}
          badge={failedCount > 0 ? failedCount : undefined}
          onSelect={launch(onOpenLog)}
        />

        <div className="more-sheet-divider" role="separator" />
        <p className="more-sheet-section">{t("nav.moreSystem")}</p>
        {noDownloader && (
          <button type="button" className="more-sheet-status-row" onClick={launch(onOpenDownloaderSettings)}>
            <span aria-hidden className="system-status-dot system-status-dot-warning" />
            <span className="more-sheet-row-label">{t("nav.statusNoDownloader")}</span>
            <span className="system-status-row-action">{t("nav.goSettings")}</span>
          </button>
        )}
        <div className="more-sheet-status">
          {statusConnections.map((connection) => (
            <span key={connection.id} className="more-sheet-status-item">
              <span
                aria-hidden
                className={cn("system-status-dot", connection.enabled ? "system-status-dot-enabled" : "system-status-dot-disabled")}
              />
              {CONNECTION_LABEL()[connection.kind]}
            </span>
          ))}
        </div>
        <p className="more-sheet-meta">
          <CalendarDays width={12} height={12} aria-hidden />
          {nextRun
            ? `${t("nav.statusNextRun")} · ${AUTOMATION_KIND_LABEL()[nextRun.kind] ?? nextRun.kind} · ${formatDateTime(nextRun.next_run_at)}`
            : t("nav.automationEmpty")}
        </p>

        <div className="more-sheet-divider" role="separator" />
        <MoreRow icon={<Settings width={18} height={18} />} label={t("nav.settings")} onSelect={launch(onOpenSettings)} />
        <div className="more-sheet-choice">
          <span className="more-sheet-choice-label">{t("account.appearance")}</span>
          <Segmented
            size="compact"
            aria-label={t("account.appearance")}
            value={(theme === "dark" || theme === "system" ? theme : "light") as "light" | "dark" | "system"}
            onValueChange={(next) => setTheme(next)}
            options={[
              { value: "light", label: t("account.light") },
              { value: "dark", label: t("account.dark") },
              { value: "system", label: t("account.system") },
            ]}
          />
        </div>
        <div className="more-sheet-choice">
          <span className="more-sheet-choice-label">{t("account.language")}</span>
          <Segmented
            size="compact"
            aria-label={t("account.language")}
            value={locale}
            onValueChange={(next) => {
              if (isLocale(next)) setLocale(next);
            }}
            // Language names are written in their own locale, never translated with the UI language.
            options={LOCALES.map((value) => ({ value, label: <span lang={value}>{LOCALE_NAMES[value]}</span> }))}
          />
        </div>
        <MoreRow icon={<LogOut width={18} height={18} />} label={t("account.logout")} onSelect={onLogout} external={false} />
      </div>
    </Dialog>
  );
}

function MoreRow({
  icon,
  label,
  badge,
  onSelect,
  external = true,
}: {
  icon: React.ReactNode;
  label: string;
  badge?: number;
  onSelect: () => void;
  external?: boolean;
}) {
  return (
    <button type="button" className="more-sheet-row" onClick={onSelect}>
      <span className="more-sheet-row-icon" aria-hidden>
        {icon}
      </span>
      <span className="more-sheet-row-label">{label}</span>
      {badge !== undefined && (
        <StatePill tone="offline">
          {badgeText(badge)}
        </StatePill>
      )}
      {external && <ArrowUpRight className="more-sheet-row-opn" aria-hidden />}
    </button>
  );
}
