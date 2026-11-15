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
      description: "Insert an image attachment. Automatically uses a provider cover, bound Bangumi/TMDB poster, or built-in placeholder.",
    },
    bgm: {
      link: { label: "Bangumi link", description: "Safe page URL of the bound Bangumi entry." },
      image: { label: "Bangumi image", description: "Use the bound Bangumi poster, falling back to the built-in placeholder." },
    },
    tmdb: {
      link: { label: "TMDB link", description: "Safe page URL of the bound TMDB entry." },
      image: { label: "TMDB image", description: "Use the bound TMDB poster, falling back to the built-in placeholder." },
    },
    anibt: {
      link: { label: "AniBT release link", description: "Safe page URL of the current AniBT release; empty for other providers." },
      image: { label: "AniBT image", description: "Use the current AniBT work cover, falling back to the built-in placeholder." },
    },
    mikan: {
      link: { label: "Mikan release link", description: "Safe page URL of the current Mikan release; empty for other providers." },
      image: { label: "Mikan image", description: "Use the current Mikan work cover, falling back to the built-in placeholder." },
    },
    release: {
      link: { label: "Release page", description: "Safe details page URL of the current release; empty when no explicit page is recorded." },
    },
    download: {
      link: { label: "Download link", description: "Download URL without a passkey, token, or other credential; empty when no URL is safe to share." },
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
