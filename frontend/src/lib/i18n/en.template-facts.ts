// Template variable catalog (shared by naming and notifications): groups,
// search and availability. Each variable's own label and
// description come from the generated facts.generated.ts.

import type { CopyShape } from "./types";
import type { templateFactsCopy } from "./zh-CN.template-facts";

export const enTemplateFactsCopy = {
  searchLabel: "Search variables",
  searchPlaceholder: "Search by name or path",
  noMatch: "No matching variables",
  insertHint: "Click to insert into the template being edited",
  phaseExecution: "At run time",
  phaseExecutionHint: "Has a value only when the file is processed; previews show a placeholder",
  nullable: "May be empty",
  example: "e.g. {value}",
  placeholderHint: "Known after the download: {path}",
  placeholder: "⟨{label}⟩",
  semantic: {
    image: {
      label: "Show image",
      description: "Insert an image attachment. Uses the provider cover, bound Bangumi/TMDB poster, or built-in placeholder. Template-provided image URLs are not accepted.",
    },
    link: {
      label: "Metadata links",
      description: "Insert links to bound Bangumi/TMDB entries. No link is emitted without a valid binding.",
    },
  },
  roots: {
    image: "Attachments",
    link: "Links",
    a: "Anime",
    r: "Release",
    t: "Torrent",
    f: "File",
    m: "Media",
    frt: "File-name reltitle values",
    anime: "Anime",
    bgm: "Bangumi",
    tmdb: "TMDB",
    release: "Release",
    ep: "Episode",
    sub: "Subtitle",
    torrent: "Torrent",
    file: "File",
    out: "Library target",
    op: "File operation",
    event: "Event",
    result: "Result",
    download: "Download",
    rt: "reltitle values",
    anibt: "AniBT tags",
    vars: "Custom variables",
    mikan: "Mikan source group",
    media: "Media",
    release_title: "Release title",
    files: "File list",
  },
} as const satisfies CopyShape<typeof templateFactsCopy>;
