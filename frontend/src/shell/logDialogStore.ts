import { create } from "zustand";

// LogDialog open state: shared by the top-bar entry and the anime card's "log"
// secondary action. The card entry carries a managed_anime_id as the preselected
// anime filter for events; the anime identity is only an optional filter, never
// a per-anime log (one global entry, the anime filter optional).

export const useLogDialogStore = create<{
  open: boolean;
  /** Anime filter preselected by the card entry; null from the top-bar entry (all anime). */
  managedAnimeId: string | null;
  openDialog: (managedAnimeId?: string) => void;
  closeDialog: () => void;
}>((set) => ({
  open: false,
  managedAnimeId: null,
  openDialog: (managedAnimeId) => set({ open: true, managedAnimeId: managedAnimeId ?? null }),
  closeDialog: () => set({ open: false, managedAnimeId: null }),
}));
