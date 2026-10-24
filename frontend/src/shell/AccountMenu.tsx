import { useTheme } from "next-themes";
import { Languages, LogOut, Moon, Monitor, Sun, SunMoon, UserRound } from "lucide-react";
import { isLocale, LOCALE_NAMES, LOCALES, setLocale, t, useLocale } from "../lib/i18n";
import { DropdownMenu, type MenuEntry } from "../ui/base";

// The desktop account menu: account, appearance, language, sign out. Hidden
// anime live in Settings.
export function AccountMenu({
  username,
  onLogout,
  open,
  onOpenChange,
}: {
  username: string | null;
  onLogout: () => void;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const { theme, setTheme } = useTheme();
  const locale = useLocale();
  const items: MenuEntry[] = [
    {
      key: "account",
      disabled: true,
      icon: <UserRound className="account-menu-icon" />,
      label: username ?? t("account.notLoggedIn"),
      onSelect: () => {},
    },
    { type: "divider", key: "divider-account" },
    {
      type: "submenu",
      key: "theme",
      icon: <SunMoon className="account-menu-icon" />,
      label: t("account.appearance"),
      items: [
        {
          type: "radio",
          key: "theme-choice",
          value: theme ?? "light",
          onValueChange: (next) => {
            if (next === "light" || next === "dark" || next === "system") setTheme(next);
          },
          options: [
            { value: "light", label: t("account.light"), icon: <Sun className="account-menu-icon" /> },
            { value: "dark", label: t("account.dark"), icon: <Moon className="account-menu-icon" /> },
            { value: "system", label: t("account.system"), icon: <Monitor className="account-menu-icon" /> },
          ],
        },
      ],
    },
    {
      type: "submenu",
      key: "language",
      icon: <Languages className="account-menu-icon" />,
      label: t("account.language"),
      items: [
        {
          type: "radio",
          key: "language-choice",
          value: locale,
          onValueChange: (next) => {
            if (isLocale(next)) setLocale(next);
          },
          // Language names are written in their own locale, never translated with the UI language.
          options: LOCALES.map((value) => ({ value, label: <span lang={value}>{LOCALE_NAMES[value]}</span> })),
        },
      ],
    },
    { type: "divider", key: "divider-logout" },
    {
      key: "logout",
      icon: <LogOut className="account-menu-icon" />,
      label: t("account.logout"),
      onSelect: onLogout,
    },
  ];

  return (
    <DropdownMenu
      open={open}
      onOpenChange={onOpenChange}
      trigger={
        <button
          type="button"
          aria-label={t("nav.account")}
          className="top-nav-icon-btn"
          title={t("nav.account")}
        >
          <span className="account-avatar" aria-hidden>
            {username?.trim().charAt(0).toUpperCase() || "?"}
          </span>
        </button>
      }
      items={items}
    />
  );
}
