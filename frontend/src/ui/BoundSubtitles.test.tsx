import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { t } from "../lib/i18n";
import { labelValue } from "../lib/i18n/format";
import { BoundSubtitles, type BoundSubtitleItem } from "./BoundSubtitles";
import { fileUnitsSummary, groupBoundSubtitles, subtitleFormat } from "../lib/boundSubtitles";

type File = { path: string; bound?: string };

describe("groupBoundSubtitles", () => {
  const pathOf = (file: File) => file.path;
  const boundOf = (file: File) => file.bound;

  it("nests subtitles under the video the server bound them to, keeping order", () => {
    const files: File[] = [
      { path: "Show/01.mkv" },
      { path: "Show/02.mkv" },
      { path: "Show/02.sc.ass", bound: "Show/02.mkv" },
      { path: "Show/01.sc.ass", bound: "Show/01.mkv" },
      { path: "Show/01.tc.ass", bound: "Show/01.mkv" },
    ];
    expect(groupBoundSubtitles(files, pathOf, boundOf).map(({ file, subtitles }) => [file.path, subtitles.map(pathOf)])).toEqual([
      ["Show/01.mkv", ["Show/01.sc.ass", "Show/01.tc.ass"]],
      ["Show/02.mkv", ["Show/02.sc.ass"]],
    ]);
  });

  it("keeps unbound subtitles, other files and subtitles bound to a video outside the list as flat rows", () => {
    const files: File[] = [
      { path: "Show/01.mkv" },
      { path: "Show/notes.ass" },
      { path: "Show/cover.jpg" },
      { path: "Show/02.ass", bound: "Show/02.mkv" },
      // Looks like the subtitle for 01.mkv by name, but the server bound nothing: never infer from file names.
      { path: "Show/01.ass" },
    ];
    expect(groupBoundSubtitles(files, pathOf, boundOf).map(({ file, subtitles }) => [file.path, subtitles.length])).toEqual([
      ["Show/01.mkv", 0],
      ["Show/notes.ass", 0],
      ["Show/cover.jpg", 0],
      ["Show/02.ass", 0],
      ["Show/01.ass", 0],
    ]);
  });
});

const SUBTITLES: BoundSubtitleItem[] = [
  { key: "a", path: "Show/01.sc.ass", sizeBytes: 2048, language: "zh-Hans", included: true, target: "/library/Show/Show - 01.zh-Hans.ass" },
  { key: "b", path: "Show/01.jp.ass", sizeBytes: 1024, language: "ja", included: false, reasonCode: "file_not_in_matched" },
  { key: "c", path: "Show/01.tc.srt", included: true, targetProblem: "等待关联" },
  { key: "d", path: "Show/01.ass", sizeBytes: 512, included: false, reasonCode: "target_duplicate" },
];

const badges = () => screen.getAllByRole("listitem").filter((item) => item.hasAttribute("data-subtitle-badge"));

describe("subtitleFormat", () => {
  it("is the uppercase extension and never empty", () => {
    expect(subtitleFormat("Show/01.sc.ass")).toBe("ASS");
    expect(subtitleFormat("Show/01.Vtt")).toBe("VTT");
    expect(subtitleFormat("Show/README")).toBe(t("ui.subtitles.formatUnknown"));
    expect(subtitleFormat("Show/.ass")).toBe(t("ui.subtitles.formatUnknown"));
  });
});

describe("BoundSubtitles", () => {
  it("renders nothing without subtitles", () => {
    const { container } = render(<BoundSubtitles videoName="Show/01.mkv" subtitles={[]} />);
    expect(container).toBeEmptyDOMElement();
  });

  it("shows one format badge per subtitle in file order, collapsed by default", () => {
    render(<BoundSubtitles videoName="Show/01.mkv" subtitles={SUBTITLES} />);
    expect(badges().map((badge) => badge.textContent?.slice(0, 3))).toEqual(["ASS", "ASS", "SRT", "ASS"]);
    expect(badges().map((badge) => badge.dataset.subtitleBadge)).toEqual(["placed", "otherReason", "noTarget", "duplicate"]);
    const toggle = screen.getByRole("button", { name: t("ui.subtitles.expand", { name: "Show/01.mkv", n: 4 }) });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    // Paths never appear on the badge row.
    expect(screen.queryByText(/Show\/01\.sc\.ass/)).toBeNull();
    expect(screen.queryByText(/→/)).toBeNull();
  });

  it("strikes a same-name badge, warns on a missing target and shows a legacy reason neutrally", () => {
    render(<BoundSubtitles videoName="Show/01.mkv" subtitles={SUBTITLES} />);
    const [placed, legacy, noTarget, duplicate] = badges();
    expect(placed!.querySelector("s")).toBeNull();
    expect(placed!.querySelector("[tabindex]")).toBeNull();
    // A bound subtitle is never filtered on its own: a rule reason from legacy data
    // is stated neutrally — no "filtered" wording, no strikethrough.
    expect(legacy!.querySelector("s")).toBeNull();
    expect(legacy).toHaveTextContent(t("reason.file_not_in_matched"));
    expect(legacy).not.toHaveTextContent(t("verdict.filteredWithReason", { reason: t("reason.file_not_in_matched") }));
    expect(legacy!.querySelector("[tabindex='0']")).not.toBeNull();
    expect(noTarget!.querySelector("s")).toBeNull();
    expect(noTarget!.querySelector("[tabindex='0']")).not.toBeNull();
    expect(noTarget).toHaveTextContent("等待关联");
    expect(duplicate!.querySelector("s")).toHaveTextContent("ASS");
    expect(duplicate).toHaveTextContent(t("ui.subtitles.duplicate"));
  });

  it("renders a bare subtitle as its format alone", () => {
    render(<BoundSubtitles videoName="Show/01.mkv" subtitles={[{ key: "x", path: "Show/01.ass", included: true, target: "/library/Show/Show - 01.ass" }]} />);
    expect(badges()).toHaveLength(1);
    expect(badges()[0]).toHaveTextContent(/^ASS$/);
  });

  it("follows an excluded video: neutral badges without a reason of their own", () => {
    render(<BoundSubtitles videoName="Show/01.mkv" subtitles={SUBTITLES.slice(0, 2)} videoExcluded />);
    expect(badges().map((badge) => badge.dataset.subtitleBadge)).toEqual(["followsVideo", "followsVideo"]);
    expect(screen.queryByText(t("reason.file_not_in_matched"))).toBeNull();
    for (const badge of badges()) {
      expect(badge.querySelector("s")).toBeNull();
      expect(badge.querySelector("[tabindex]")).toBeNull();
    }
    fireEvent.click(screen.getByRole("button", { name: t("ui.subtitles.expand", { name: "Show/01.mkv", n: 2 }) }));
    expect(screen.getAllByText(t("reason.subtitle_video_excluded"))).toHaveLength(2);
  });

  it("expands to each subtitle's source, size, language and target or reason", () => {
    render(<BoundSubtitles videoName="Show/01.mkv" subtitles={SUBTITLES} />);
    fireEvent.click(screen.getByRole("button", { name: t("ui.subtitles.expand", { name: "Show/01.mkv", n: 4 }) }));
    const toggle = screen.getByRole("button", { name: t("ui.subtitles.collapse", { name: "Show/01.mkv", n: 4 }) });
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    const panel = document.getElementById(toggle.getAttribute("aria-controls")!)!;
    const items = within(panel).getAllByRole("listitem");
    expect(items).toHaveLength(4);
    expect(items[0]).toHaveTextContent("Show/01.sc.ass");
    expect(items[0]).toHaveTextContent("2.0 KB");
    expect(items[0]).toHaveTextContent(labelValue(t("ui.subtitles.language"), "zh-Hans"));
    expect(within(items[0]!).getByText("→ /library/Show/Show - 01.zh-Hans.ass")).toBeVisible();
    expect(items[1]).toHaveTextContent(t("reason.file_not_in_matched"));
    expect(items[1]).not.toHaveTextContent(t("verdict.filteredWithReason", { reason: t("reason.file_not_in_matched") }));
    expect(items[1]).not.toHaveTextContent("→");
    expect(items[2]).toHaveTextContent(t("ui.subtitles.noLanguage"));
    expect(items[2]).toHaveTextContent("等待关联");
    expect(items[3]).toHaveTextContent(t("ui.subtitles.duplicate"));

    fireEvent.click(toggle);
    expect(document.getElementById(toggle.getAttribute("aria-controls")!)).toBeNull();
  });
});

describe("BoundSubtitles language source", () => {
  it("names where the suffix language came from", () => {
    render(
      <BoundSubtitles
        videoName="Show/01.mkv"
        subtitles={[
          { key: "a", path: "Show/01.chs&jpn.ass", included: true, target: "/library/Show/Show - 01.zh-Hans.ass", language: "zh-Hans", languageSource: "file" },
          { key: "b", path: "Show/01.ass", included: true, target: "/library/Show/Show - 01.zh.ass", language: "zh", languageSource: "release" },
        ]}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: /Show\/01\.mkv/ }));
    expect(screen.getByText(new RegExp(t("ui.subtitles.languageFromFile")))).toBeInTheDocument();
    expect(screen.getByText(new RegExp(t("ui.subtitles.languageFromRelease")))).toBeInTheDocument();
  });
});

describe("fileUnitsSummary", () => {
  type F = { path: string; kind?: string; included: boolean; target?: string; bound?: string };
  const summary = (files: F[]) => fileUnitsSummary(groupBoundSubtitles(files, (f) => f.path, (f) => f.bound), (f) => ({ kind: f.kind, included: f.included, target: f.target }));

  it("counts video units with their subtitles alongside", () => {
    expect(summary([
      { path: "01.mkv", kind: "video", included: true, target: "/a/01.mkv" },
      { path: "01.sc.ass", kind: "subtitle", included: true, target: "/a/01.zh.ass", bound: "01.mkv" },
      { path: "01.tc.ass", kind: "subtitle", included: true, target: "/a/01.zh-Hant.ass", bound: "01.mkv" },
    ])).toBe([t("ui.fileUnits.videos", { n: 1 }), t("ui.fileUnits.subtitles", { n: 2 })].join(" · "));
  });

  it("shows placed/bound when some subtitles are not placed, and other files on their own", () => {
    expect(summary([
      { path: "01.mkv", kind: "video", included: true, target: "/a/01.mkv" },
      { path: "01.sc.ass", kind: "subtitle", included: true, target: "/a/01.zh.ass", bound: "01.mkv" },
      { path: "01.jp.ass", kind: "subtitle", included: false, bound: "01.mkv" },
      { path: "notes.ass", kind: "subtitle", included: false },
      { path: "02.mkv", kind: "video", included: false },
      { path: "02.ass", kind: "subtitle", included: true, target: "/a/02.ass", bound: "02.mkv" },
    ])).toBe([
      t("ui.fileUnits.videosPartial", { placed: 1, total: 2 }),
      t("ui.fileUnits.subtitlesPartial", { placed: 1, total: 3 }),
      t("ui.fileUnits.others", { n: 1 }),
    ].join(" · "));
  });

  it("falls back to a plain file count without videos", () => {
    expect(summary([{ path: "a.ass", kind: "subtitle", included: false }])).toBe(t("ui.fileUnits.files", { n: 1 }));
  });
});
