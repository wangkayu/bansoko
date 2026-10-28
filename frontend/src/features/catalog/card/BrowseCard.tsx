import { EyeOff, MoreHorizontal, ScanSearch, X } from "lucide-react";
import type { CatalogEntryView } from "../../../lib/providers/viewmodels";
import { DropdownMenu, Tooltip, type MenuEntry } from "../../../ui/base";
import { Poster } from "./Poster";
import { CornerBadges } from "./CornerBadges";
import { t } from "../../../lib/i18n";
import { lifecycleStateLabel } from "../../../lib/labels";

// The browse card (shared by the catalog browse areas and global search results, after the card
//   redesign): a 2:3 poster with a fixed information budget — one title line + the original name +
//   one caption line (group count / updated) + top-left badges (broadcast day / score / state).
//   The card face has no full-width button: the whole card opens the config — a provider entry
//   with no live subscription opens a candidate draft (subscribing completes there); one with a live
//   (non-retired) subscription opens that subscription instead, or the subscription column filtered
//   to the entry when there are several (the host decides, see useOpenLiveSubscription); a retired-only
//   entry opens a draft again. A subscribed custom preset opens its subscription configuration.
//   Browse (the one-shot download's browse-seeds entry) is a persistent low-contrast small icon
//   button at the strip's right end, a hit area of its own, never opening the config; hiding and
//   deleting a preset live in the top-right menu.
//   Hidden anime enter no list at all, so the card carries no hidden state and no unhide entry
//   (those live only in settings' hidden-anime section).
//   Subscribed-or-not is a boolean, never the subscription's state: with a subscription the card
//   simply adds a subscribed badge at the top left; browse and hide behave exactly like an
//   unsubscribed candidate. Subscription state, progress, RSS signals and subscription actions
//   belong to the subscribed wall and never render here.

function captionParts(entry: CatalogEntryView): string[] {
  const parts: string[] = [];
  if (entry.source === "custom" && entry.instanceLabel) {
    parts.push(entry.instanceLabel);
  }
  if (entry.releaseCount !== undefined && entry.releaseState !== "no_releases") {
    parts.push(t("grid.card.releaseCount", { n: entry.releaseCount }));
  }
  if (entry.lastUpdated) parts.push(t("grid.card.updatedAt", { date: entry.lastUpdated }));
  return parts;
}

export function BrowseCard({
  entry,
  onHide,
  onOpenConfig,
  onDeleteCandidate,
  onBrowseSeeds,
}: {
  entry: CatalogEntryView;
  /** Hides a provider entry; custom goes through delete-preset, and the subscription list and search results pass none. */
  onHide?: (entry: CatalogEntryView) => void;
  /** Opens on a card click. The host routes a provider entry with live instances (liveInstanceRefs)
   *  to its subscription before falling back to the candidate draft; a subscribed custom preset
   *  opens its subscription configuration. */
  onOpenConfig: (entry: CatalogEntryView) => void;
  /** Deletes an unsubscribed custom preset; a subscribed preset shows nothing. */
  onDeleteCandidate?: (entry: CatalogEntryView) => void;
  /** Opens the two-step browse-seeds dialog (the one-shot download's separate entry); not passed means not rendered. */
  onBrowseSeeds?: (entry: CatalogEntryView) => void;
}) {
  const retired = entry.lifecycleState === "retired";
  const isCustom = entry.source === "custom";
  // Only subscribed custom presets carry a lifecycle: a retired preset still hangs onto its retired
//   owner, and re-subscribing would replay the consumed candidate — so it has no openable target and
//   the explicit path is deleting the preset. Provider candidate cards carry no subscription fields
//   and are always openable.
  const canOpen = !retired;
  const subscribed = !retired && (entry.subscribed === true || entry.subscriptionState !== undefined);
  const caption = captionParts(entry);
  const noReleases = entry.releaseState === "no_releases";
  // A read-only entry: writes no subscription and no rules. Custom entries need their supplied preset; a retired custom preset offers none.
  const canBrowseSeeds = Boolean(onBrowseSeeds) && !(retired && isCustom) && (!isCustom || Boolean(entry.candidateId));
  const browseSeeds = () => onBrowseSeeds?.(entry);

  // The menu: hide and delete preset.
  const menuItems: MenuEntry[] = [
    // Hiding lands on the server's (provider, provider_entry_ref), so only provider entries can hide;
//     custom presets go through delete-preset. Hiding is catalog curation: it ends no subscription.
    ...(!isCustom && onHide
      ? [
          {
            key: "hide",
            icon: <EyeOff className="catalog-pw-menu-icon" />,
            label: t("grid.card.hide"),
            onSelect: () => onHide(entry),
          },
        ]
      : []),
    ...(!subscribed && !retired && isCustom && onDeleteCandidate && entry.candidateId
      ? [
          {
            key: "delete-preset",
            danger: true,
            icon: <X className="catalog-pw-menu-icon" />,
            label: t("grid.card.deletePreset"),
            onSelect: () => onDeleteCandidate(entry),
          },
        ]
      : []),
  ];

  // The top-left state pill (the same chip as the subscribed poster card): retired / subscribed.
  const statusPill = retired ? (
    <span className="catalog-pw-chip catalog-pw-chip-muted">{lifecycleStateLabel("retired")}</span>
  ) : subscribed ? (
    <span className="catalog-pw-chip catalog-pw-chip-online">{t("grid.card.subscribed")}</span>
  ) : null;

  return (
    <article
      className={
        "browse-card " +
        (subscribed ? "browse-card-subscribed " : "") +
        (noReleases ? "browse-card-no-releases" : "")
      }
    >
      <Poster entry={entry} scrim />
      {/* The site states there are no releases yet: the poster dims under a centered short label, matching Mikan/AniBT's mask; clicks pass through. */}
      {noReleases && <span className="browse-card-no-releases-label">{t("grid.card.noReleases")}</span>}

      {/* The primary click area: the whole card is open-config (one focusable control), with browse
          and the menu as its siblings — no nested interaction. */}
      {canOpen && (
        <button
          type="button"
          aria-label={t("grid.card.openConfig", { title: entry.title })}
          title={entry.title}
          onClick={() => onOpenConfig(entry)}
          className="browse-card-open"
        />
      )}

      <CornerBadges entry={entry}>
        {statusPill}
      </CornerBadges>

      {menuItems.length > 0 && (
        <DropdownMenu
          items={menuItems}
          trigger={
            <button type="button" aria-label={t("grid.card.moreFor", { title: entry.title })} title={t("grid.card.more")} className="catalog-pw-more">
              <MoreHorizontal aria-hidden className="catalog-pw-more-icon" />
            </button>
          }
        />
      )}

      {/* The bottom info strip: title + original name + caption; the browse icon sits at its right end (a hit area of its own). */}
      <div className="browse-card-overlay">
        <div className="browse-card-text">
          <h3 className="browse-card-title" title={entry.title}>
            {entry.title}
          </h3>
          {entry.originalTitle && (
            <p className="browse-card-caption text-caption" title={entry.originalTitle}>
              {entry.originalTitle}
            </p>
          )}
          {caption.length > 0 && <p className="browse-card-caption text-caption">{caption.join(" · ")}</p>}
        </div>
        {canBrowseSeeds && (
          <Tooltip content={t("grid.card.browse")}>
            <button
              type="button"
              aria-label={t("oneshot.actionFor", { title: entry.title })}
              onClick={browseSeeds}
              className="browse-card-browse"
            >
              <ScanSearch aria-hidden className="browse-card-browse-icon" />
            </button>
          </Tooltip>
        )}
      </div>
    </article>
  );
}
