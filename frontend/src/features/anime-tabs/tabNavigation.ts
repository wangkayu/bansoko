import { createContext, useContext } from "react";

// Lets content inside one anime's tabs open a sibling tab (the retention
// editor's subgroup score item links to the subgroups tab). Outside the anime
// tabs (Global settings) there is no navigator.

export type AnimeTabKey = "metadata" | "subgroups" | "rules" | "rss";

export const AnimeTabNavigationContext = createContext<((tab: AnimeTabKey) => void) | undefined>(undefined);

export function useAnimeTabNavigation(): ((tab: AnimeTabKey) => void) | undefined {
  return useContext(AnimeTabNavigationContext);
}
