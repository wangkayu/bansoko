import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { t } from "../../lib/i18n";
import { FileNumberingDialog } from "./FileNumberingDialog";
import type { FileEpisodeOverride, FileNumberingItem } from "./model";

const items: FileNumberingItem[] = [
  { path: "Pack/S01/01.mkv", sizeBytes: 100, automaticSeason: 1, automaticEpisode: 1, automaticLabel: "自动 · 第 1 集", subtitleCount: 1 },
  { path: "Pack/S01/02.mkv", sizeBytes: 100, automaticSeason: 1, automaticEpisode: 2, automaticLabel: "自动 · 第 2 集", subtitleCount: 0 },
  { path: "Pack/S02/01.mkv", sizeBytes: 100, automaticSeason: 1, automaticEpisode: 1, automaticLabel: "自动 · 第 1 集", subtitleCount: 0 },
  { path: "Pack/SP/01.mkv", sizeBytes: 100, automaticSeason: 1, automaticEpisode: null, automaticLabel: "自动 · 未识别", subtitleCount: 0 },
];
const baseline: FileEpisodeOverride[] = [{ path: "Pack/S01/01.mkv", season: 1, episode: 11 }];

function setup(overrides = baseline, dialogItems = items) {
  const onApply = vi.fn();
  const onClose = vi.fn();
  const view = render(<FileNumberingDialog title="示例种子" items={dialogItems} overrides={overrides} onApply={onApply} onClose={onClose} />);
  return { onApply, onClose, unmount: view.unmount };
}

async function check(path: string) {
  await userEvent.click(screen.getByRole("checkbox", { name: t("fileNumbering.checkRow", { path }) }));
}

describe("FileNumberingDialog", () => {
  it("keeps and applies every draft across filters and batch-selection changes", async () => {
    const { onApply, onClose } = setup();
    await userEvent.clear(screen.getByLabelText(`${t("fileNumbering.season")} Pack/S01/01.mkv`));
    await userEvent.type(screen.getByLabelText(`${t("fileNumbering.season")} Pack/S01/01.mkv`), "11");
    await userEvent.clear(screen.getByLabelText(`${t("fileNumbering.episode")} Pack/S01/01.mkv`));
    await userEvent.type(screen.getByLabelText(`${t("fileNumbering.episode")} Pack/S01/01.mkv`), "21");

    await userEvent.type(screen.getByLabelText(t("fileNumbering.filterLabel")), "S02");
    expect(screen.queryByText("Pack/S01/01.mkv", { exact: false })).not.toBeInTheDocument();
    await check("Pack/S02/01.mkv");
    await userEvent.clear(screen.getByLabelText(`${t("fileNumbering.season")} Pack/S02/01.mkv`));
    await userEvent.type(screen.getByLabelText(`${t("fileNumbering.season")} Pack/S02/01.mkv`), "2");
    await userEvent.clear(screen.getByLabelText(`${t("fileNumbering.episode")} Pack/S02/01.mkv`));
    await userEvent.type(screen.getByLabelText(`${t("fileNumbering.episode")} Pack/S02/01.mkv`), "22");
    await userEvent.click(screen.getByRole("button", { name: t("fileNumbering.confirm", { n: 2 }) }));
    expect(onApply).toHaveBeenCalledWith([
      { path: "Pack/S01/01.mkv", season: 11, episode: 21 },
      { path: "Pack/S02/01.mkv", season: 2, episode: 22 },
    ]);
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("keeps twelve ranged batch drafts when an unchecked special is edited", async () => {
    const pack: FileNumberingItem[] = [
      ...Array.from({ length: 12 }, (_, index): FileNumberingItem => ({
        path: `Pack/A${String(index + 1).padStart(2, "0")}.mkv`,
        automaticSeason: 1,
        automaticEpisode: index + 1,
        automaticLabel: `自动 · 第 ${index + 1} 集`,
        subtitleCount: 0,
      })),
      { path: "Pack/EX.mkv", automaticSeason: 1, automaticEpisode: null, automaticLabel: "自动 · 未识别", subtitleCount: 0 },
    ];
    const { onApply } = setup([], pack);
    await userEvent.type(screen.getByLabelText(t("fileNumbering.filterLabel")), "Pack/A");
    await userEvent.click(screen.getByRole("button", { name: t("fileNumbering.selectFiltered") }));
    await userEvent.clear(screen.getByLabelText(t("fileNumbering.sequenceSpan")));
    await userEvent.type(screen.getByLabelText(t("fileNumbering.sequenceSpan")), "2");
    await userEvent.click(screen.getByRole("button", { name: t("fileNumbering.fillSequence") }));
    await userEvent.click(screen.getByRole("button", { name: t("fileNumbering.unselectFiltered") }));

    await userEvent.clear(screen.getByLabelText(t("fileNumbering.filterLabel")));
    await userEvent.type(screen.getByLabelText(t("fileNumbering.filterLabel")), "EX");
    await userEvent.clear(screen.getByLabelText(`${t("fileNumbering.season")} Pack/EX.mkv`));
    await userEvent.type(screen.getByLabelText(`${t("fileNumbering.season")} Pack/EX.mkv`), "0");
    await userEvent.clear(screen.getByLabelText(`${t("fileNumbering.episode")} Pack/EX.mkv`));
    await userEvent.type(screen.getByLabelText(`${t("fileNumbering.episode")} Pack/EX.mkv`), "1-2");
    await userEvent.click(screen.getByRole("button", { name: t("fileNumbering.confirm", { n: 13 }) }));

    expect(onApply).toHaveBeenCalledOnce();
    const applied = onApply.mock.calls[0][0] as FileEpisodeOverride[];
    expect(applied).toHaveLength(13);
    expect(applied[0]).toEqual({ path: "Pack/A01.mkv", season: 1, episode: 1, episode_end: 2 });
    expect(applied[11]).toEqual({ path: "Pack/A12.mkv", season: 1, episode: 23, episode_end: 24 });
    expect(applied[12]).toEqual({ path: "Pack/EX.mkv", season: 0, episode: 1, episode_end: 2 });
  });

  it("restores all drafts, restores checked batches, and supports cancellation", async () => {
    const first = setup();
    await userEvent.click(screen.getByRole("button", { name: t("fileNumbering.restoreAll") }));
    expect(screen.getByRole("button", { name: t("fileNumbering.confirm", { n: 0 }) })).toBeEnabled();
    await userEvent.click(screen.getByRole("button", { name: t("act.cancel") }));
    expect(first.onApply).not.toHaveBeenCalled();
    first.unmount();

    const { onApply } = setup();
    await check("Pack/S01/01.mkv");
    await userEvent.click(screen.getByRole("button", { name: t("fileNumbering.restoreChecked") }));
    await userEvent.click(screen.getByRole("button", { name: t("fileNumbering.confirm", { n: 0 }) }));
    expect(onApply).toHaveBeenCalledWith([]);
  });

  it("allows direct editing without checking and an empty season inherits the main setting", async () => {
    const { onApply } = setup();
    const season = screen.getByLabelText(`${t("fileNumbering.season")} Pack/S01/02.mkv`);
    const episode = screen.getByLabelText(`${t("fileNumbering.episode")} Pack/S01/02.mkv`);
    expect(season).toBeEnabled();
    expect(episode).toBeEnabled();
    await userEvent.clear(season);
    await userEvent.clear(episode);
    await userEvent.type(episode, "9");
    await userEvent.click(screen.getByRole("button", { name: t("fileNumbering.confirm", { n: 2 }) }));
    expect(onApply).toHaveBeenCalledWith([
      { path: "Pack/S01/01.mkv", season: 1, episode: 11 },
      { path: "Pack/S01/02.mkv", episode: 9 },
    ]);
  });

  it("blocks any invalid draft and can restore that unchecked row", async () => {
    const { onApply } = setup([]);
    await userEvent.type(screen.getByLabelText(`${t("fileNumbering.episode")} Pack/SP/01.mkv`), "bad");
    await userEvent.clear(screen.getByLabelText(`${t("fileNumbering.episode")} Pack/S02/01.mkv`));
    await userEvent.type(screen.getByLabelText(`${t("fileNumbering.episode")} Pack/S02/01.mkv`), "4");
    await userEvent.click(screen.getByRole("button", { name: t("fileNumbering.confirm", { n: 2 }) }));
    expect(screen.getByRole("alert")).toHaveTextContent(t("fileNumbering.invalid"));
    expect(onApply).not.toHaveBeenCalled();

    const specialRow = document.querySelector('[data-numbering-row="Pack/SP/01.mkv"]');
    expect(specialRow).not.toBeNull();
    await userEvent.click(within(specialRow as HTMLElement).getByRole("button", { name: t("fileNumbering.restoreRow") }));
    await userEvent.click(screen.getByRole("button", { name: t("fileNumbering.confirm", { n: 1 }) }));
    expect(onApply).toHaveBeenCalledWith([{ path: "Pack/S02/01.mkv", season: 1, episode: 4 }]);
  });

  it("fills episodes sequentially without changing seasons when bulk season is blank", async () => {
    const { onApply } = setup([]);
    await check("Pack/S01/01.mkv");
    await check("Pack/S01/02.mkv");
    await userEvent.clear(screen.getByLabelText(t("fileNumbering.sequenceStart")));
    await userEvent.type(screen.getByLabelText(t("fileNumbering.sequenceStart")), "7");
    await userEvent.click(screen.getByRole("button", { name: t("fileNumbering.fillSequence") }));

    expect(screen.getByLabelText(`${t("fileNumbering.season")} Pack/S01/01.mkv`)).toHaveValue(1);
    expect(screen.getByLabelText(`${t("fileNumbering.season")} Pack/S01/02.mkv`)).toHaveValue(1);
    await userEvent.click(screen.getByRole("button", { name: t("fileNumbering.confirm", { n: 2 }) }));
    expect(onApply).toHaveBeenCalledWith([
      { path: "Pack/S01/01.mkv", season: 1, episode: 7 },
      { path: "Pack/S01/02.mkv", season: 1, episode: 8 },
    ]);
  });

  it("fills checked files sequentially and permits duplicate final numbers", async () => {
    const { onApply } = setup([]);
    await check("Pack/S01/01.mkv");
    await check("Pack/S01/02.mkv");
    await userEvent.type(screen.getByLabelText(t("fileNumbering.bulkSeason")), "0");
    await userEvent.clear(screen.getByLabelText(t("fileNumbering.sequenceStart")));
    await userEvent.type(screen.getByLabelText(t("fileNumbering.sequenceStart")), "7");
    await userEvent.click(screen.getByRole("button", { name: t("fileNumbering.fillSequence") }));
    await check("Pack/S02/01.mkv");
    await userEvent.clear(screen.getByLabelText(`${t("fileNumbering.episode")} Pack/S02/01.mkv`));
    await userEvent.type(screen.getByLabelText(`${t("fileNumbering.episode")} Pack/S02/01.mkv`), "7");
    await userEvent.click(screen.getByRole("button", { name: t("fileNumbering.confirm", { n: 3 }) }));
    expect(onApply).toHaveBeenCalledWith([
      { path: "Pack/S01/01.mkv", season: 0, episode: 7 },
      { path: "Pack/S01/02.mkv", season: 0, episode: 8 },
      { path: "Pack/S02/01.mkv", season: 1, episode: 7 },
    ]);
  });
});
