import { Suspense } from "react";
import { lazyEntry } from "../lib/lazyEntry";
import { Navigate, Outlet, Route, Routes, useLocation } from "react-router-dom";
import { isRouteDialogEntry, ROUTE_DIALOG_STATE } from "./routeDialogs";
import { AuthGate } from "../features/auth/AuthGate";
import { t } from "../lib/i18n";

// Route table: the catalog's four sections are the only screen (AnimeGrid).
// Settings is a full-screen Dialog over the catalog, its sections still carried
// by /settings/:category, so settings deep-link, survive refresh and close on
// browser Back — the catalog stays mounted underneath and is not refetched.
// The other top-level dialogs (anime config, log, qB manager, global search)
// are route-hosted the same way, hung on the catalog base `/` as query
// parameters (?anime=&tab=, ?log=, ?tool=, coexisting with ?source=); see
// routeDialogs.ts. Nested dialogs and S-tier confirmations stay component state.
// Subscriptions have no standalone editor route — zero-config subscribes
// straight from a card (no confirmation dialog), the advanced path is the
// card's ⚙ four-tab dialog; per-anime management always goes card → detail
// dialog → config dialog (features/anime-tabs' four tabs serve both before and
// after subscribing). There is no standalone workspace page.

const AnimeGrid = lazyEntry(() => import("../features/catalog/AnimeGrid"), (m) => m.AnimeGrid);
const SettingsDialog = lazyEntry(
  () => import("../features/settings/SettingsDialogEntry"),
  (m) => m.SettingsDialogEntry,
);

// /settings shows the section list first (phones); it keeps the "opened from
// the base" marker so closing can return to the base.
function SettingsRedirect() {
  const location = useLocation();
  return (
    <Navigate
      to="/settings/connections"
      replace
      state={{ pane: "nav", ...(isRouteDialogEntry(location.state) ? ROUTE_DIALOG_STATE : {}) }}
    />
  );
}

function StageFallback() {
  return <div className="stage-fallback text-body">{t("nav.stageLoading")}</div>;
}

// The catalog is the base; overlays render on top of it through the Outlet, so
// switching to settings does not unmount AnimeGrid.
function CatalogStage() {
  return (
    <>
      <Suspense fallback={<StageFallback />}>
        <AnimeGrid />
      </Suspense>
      <Outlet />
    </>
  );
}

export function AppRoutes() {
  return (
    <Routes>
      <Route element={<AuthGate />}>
        <Route element={<CatalogStage />}>
          <Route index element={null} />
          <Route path="settings" element={<SettingsRedirect />} />
          <Route
            path="settings/:category"
            element={
              <Suspense fallback={null}>
                <SettingsDialog />
              </Suspense>
            }
          />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Route>
      </Route>
    </Routes>
  );
}
