// AniBT tag filter (per torrent): rule card, defaults, RSS list tags and filter reasons.

import type { CopyShape } from "./types";
import type { anibtTagsCopy } from "./zh-CN.anibt-tags";

export const enAnibtTagsCopy = {
  title: "AniBT Tags",
  hint: "Tags provided by AniBT; applies only to torrents from AniBT",
  role: {
    require: "Require",
    exclude: "Exclude",
  },
  roleHint: "Required tags: any one per type, and every type must match; any excluded tag filters it",
  addAs: "Add as",
  switchTo: {
    require: "Change to required: {tag}",
    exclude: "Change to excluded: {tag}",
  },
  summarySeparator: "; ",
  remove: "Remove",
  removeAria: "Remove tag {tag}",
  none: "Not filtering by tags",
  groupAria: "{dimension} tags",
  newValue: "New value",
  addDimension: "Tag type",
  addValue: "Tag value",
  addValuePlaceholder: "e.g. HEVC",
  add: "Add",
  valueEmpty: "Tag value cannot be empty",
  valueTooLong: "Tag value is too long",
  valueInvalid: "Tag value cannot contain line breaks or tabs",
  duplicate: "Tag already added",
  tooMany: "Up to {n} required and {n} excluded tags",
  rowAria: "AniBT tags of the torrent",
  reasonMissing: "AniBT tags: missing {tags}",
  reasonBlacklisted: "AniBT tags: includes {tags}",
  dimension: {
    resolution: "Resolution",
    subtitle: "Subtitles",
    language: "Language",
    format: "Format",
    codec: "Codec",
    bitdepth: "Bit depth",
    audio: "Audio",
    source: "Source",
    platform: "Platform",
    subformat: "Subtitle format",
    tag: "Other",
  },
  // Only values whose display name differs from the AniBT value are listed.
  value: {
    subtitle: {
      INTERNAL: "Soft subs",
      EMBEDDED: "Hard subs",
      NONE: "No subtitles",
    },
    language: {
      CHS: "Simplified Chinese",
      CHT: "Traditional Chinese",
      JP: "Japanese",
      EN: "English",
      ID: "Indonesian",
      TH: "Thai",
      MS: "Malay",
      VI: "Vietnamese",
      FR: "French",
      PL: "Polish",
      DE: "German",
      ES: "Spanish",
      PT: "Portuguese",
    },
    platform: {
      CR: "Crunchyroll source",
      Baha: "Bahamut source",
      "B-Global": "Bilibili Global source",
      BILIBILI: "Bilibili source",
      ABEMA: "ABEMA source",
      NF: "Netflix source",
    },
    subformat: {
      PGS: "Image-based subtitles",
    },
  },
} as const satisfies CopyShape<typeof anibtTagsCopy>;
