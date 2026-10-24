import { t } from "./i18n";

// Grouping of videos and external subtitles in the file list. The binding is the server's
// conclusion (the per-file `bound_video_source_path`); grouping here follows it and never
// re-infers from file names.

export type FileWithSubtitles<T> = { file: T; subtitles: T[] };

/**
 * 按服务端给出的绑定把字幕挂到所属视频下，保持原列表顺序。绑定指向的视频不在列表里（或指向
 * 自己）时，字幕留在原位独立成行。
 */
export function groupBoundSubtitles<T>(
  files: readonly T[],
  pathOf: (file: T) => string,
  boundVideoOf: (file: T) => string | null | undefined,
): FileWithSubtitles<T>[] {
  const paths = new Set(files.map(pathOf));
  const byVideo = new Map<string, T[]>();
  const rows: T[] = [];
  for (const file of files) {
    const video = boundVideoOf(file);
    if (video && video !== pathOf(file) && paths.has(video)) {
      byVideo.set(video, [...(byVideo.get(video) ?? []), file]);
      continue;
    }
    rows.push(file);
  }
  return rows.map((file) => ({ file, subtitles: byVideo.get(pathOf(file)) ?? [] }));
}

/** One file's counting facts: its type, whether it passes, and whether it has a target path. */
export type FileUnitFacts = { kind?: string | null; included: boolean; target?: string | null };

/**
 * 文件列表的摘要按视频单元计数：视频（及其放行数），绑定在视频下的字幕（及其入库数），
 * 其余文件（没绑定的字幕、其他类型）单独计数。字幕只有在视频放行、自己也放行且有目标路径时
 * 才算入库；视频被排除时它的字幕随视频一起不入库。
 */
export function fileUnitsSummary<T>(groups: readonly FileWithSubtitles<T>[], factsOf: (file: T) => FileUnitFacts): string {
  let videos = 0;
  let placedVideos = 0;
  let subtitles = 0;
  let placedSubtitles = 0;
  let others = 0;
  for (const { file, subtitles: bound } of groups) {
    const facts = factsOf(file);
    if (facts.kind !== "video") {
      others += 1;
      continue;
    }
    videos += 1;
    if (facts.included) placedVideos += 1;
    for (const subtitle of bound) {
      subtitles += 1;
      const sub = factsOf(subtitle);
      if (facts.included && sub.included && sub.target) placedSubtitles += 1;
    }
  }
  const parts: string[] = [];
  if (videos > 0) {
    parts.push(placedVideos === videos ? t("ui.fileUnits.videos", { n: videos }) : t("ui.fileUnits.videosPartial", { placed: placedVideos, total: videos }));
  }
  if (subtitles > 0) {
    parts.push(placedSubtitles === subtitles ? t("ui.fileUnits.subtitles", { n: subtitles }) : t("ui.fileUnits.subtitlesPartial", { placed: placedSubtitles, total: subtitles }));
  }
  if (others > 0) parts.push(videos > 0 ? t("ui.fileUnits.others", { n: others }) : t("ui.fileUnits.files", { n: others }));
  return parts.join(" · ");
}

/** The subtitle format badge's text: the uppercased extension (ASS, SRT...); a generic word when there is no extension, so no empty badge. */
export function subtitleFormat(path: string): string {
  const name = path.slice(path.lastIndexOf("/") + 1);
  const dot = name.lastIndexOf(".");
  const extension = dot > 0 ? name.slice(dot + 1).trim() : "";
  return extension ? extension.toUpperCase() : t("ui.subtitles.formatUnknown");
}
