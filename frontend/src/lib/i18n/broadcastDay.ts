import type { components } from "../api/generated";
import { t } from "./index";

// The server's broadcast day is a stable enum key (mon..sun, tbd, none; see BroadcastDay in
//   api/openapi.yaml); display copy belongs to the frontend: each key maps to the current
//   language's grid.weekday copy.

export type BroadcastDay = components["schemas"]["BroadcastDay"];

export function broadcastDayLabel(day: BroadcastDay): string {
  return t(`grid.weekday.${day}`);
}
