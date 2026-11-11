import { render, screen } from "@testing-library/react";
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

function setup(overrides = baseline) {
  const onApply = vi.fn();
  const onClose = vi.fn();
  const view = render(<FileNumberingDialog title="示例种子" items={items} overrides={overrides} onApply={onApply} onClose={onClose} />);
  return { onApply, onClose, unmount: view.unmount };
}

async function check(path: string) {
  await userEvent.click(screen.getByRole("checkbox", { name: t("fileNumbering.checkRow", { path }) }));
}

describe("FileNumberingDialog", () => {
  it("keeps drafts across filters but applies only the checked batch", async () => {
    const { onApply, onClose } = setup();
    await check("Pack/S01/01.mkv");
    await userEvent.clear(screen.getByLabelText(`${t("fileNumbering.season")} Pack/S01/01.mkv`));
    await userEvent.type(screen.getByLabelText(`${t("fileNumbering.season")} Pack/S01/01.mkv`), "11");
    await userEvent.clear(screen.getByLabelText(`${t("fileNumbering.episode")} Pack/S01/01.mkv`));
    await userEvent.type(screen.getByLabelText(`${t("fileNumbering.episode")} Pack/S01/01.mkv`), "21");
    await check("Pack/S01/01.mkv");

    await userEvent.type(screen.getByLabelText(t("fileNumbering.filterLabel")), "S02");
    expect(screen.queryByText("Pack/S01/01.mkv", { exact: false })).not.toBeInTheDocument();
    await check("Pack/S02/01.mkv");
    await userEvent.clear(screen.getByLabelText(`${t("fileNumbering.season")} Pack/S02/01.mkv`));
    await userEvent.type(screen.getByLabelText(`${t("fileNumbering.season")} Pack/S02/01.mkv`), "2");
    await userEvent.clear(screen.getByLabelText(`${t("fileNumbering.episode")} Pack/S02/01.mkv`));
    await userEvent.type(screen.getByLabelText(`${t("fileNumbering.episode")} Pack/S02/01.mkv`), "22");
    await userEvent.click(screen.getByRole("button", { name: t("fileNumbering.confirm", { n: 1 }) }));
    expect(onApply).toHaveBeenCalledWith([
      { path: "Pack/S01/01.mkv", season: 1, episode: 11 },
      { path: "Pack/S02/01.mkv", season: 2, episode: 22 },
    ]);
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("restores only checked rows after confirmation and supports cancellation", async () => {
    const first = setup();
    await userEvent.click(screen.getByRole("button", { name: t("fileNumbering.restoreAll") }));
    expect(screen.getByRole("button", { name: t("fileNumbering.confirm", { n: 4 }) })).toBeEnabled();
    await userEvent.click(screen.getByRole("button", { name: t("act.cancel") }));
    expect(first.onApply).not.toHaveBeenCalled();
    first.unmount();

    const { onApply } = setup();
    await check("Pack/S01/01.mkv");
    await userEvent.click(screen.getByRole("button", { name: t("fileNumbering.restoreChecked") }));
    await userEvent.click(screen.getByRole("button", { name: t("fileNumbering.confirm", { n: 1 }) }));
    expect(onApply).toHaveBeenCalledWith([]);
  });

  it("allows an empty season to inherit the main setting", async () => {
    const { onApply } = setup();
    await check("Pack/S01/02.mkv");
    await userEvent.clear(screen.getByLabelText(`${t("fileNumbering.season")} Pack/S01/02.mkv`));
    await userEvent.clear(screen.getByLabelText(`${t("fileNumbering.episode")} Pack/S01/02.mkv`));
    await userEvent.type(screen.getByLabelText(`${t("fileNumbering.episode")} Pack/S01/02.mkv`), "9");
    await userEvent.click(screen.getByRole("button", { name: t("fileNumbering.confirm", { n: 1 }) }));
    expect(onApply).toHaveBeenCalledWith([
      { path: "Pack/S01/01.mkv", season: 1, episode: 11 },
      { path: "Pack/S01/02.mkv", episode: 9 },
    ]);
  });

  it("does not let an invalid unchecked edit block another batch", async () => {
    const { onApply } = setup([]);
    await check("Pack/SP/01.mkv");
    await userEvent.type(screen.getByLabelText(`${t("fileNumbering.episode")} Pack/SP/01.mkv`), "bad");
    await check("Pack/SP/01.mkv");
    await check("Pack/S02/01.mkv");
    await userEvent.clear(screen.getByLabelText(`${t("fileNumbering.episode")} Pack/S02/01.mkv`));
    await userEvent.type(screen.getByLabelText(`${t("fileNumbering.episode")} Pack/S02/01.mkv`), "4");
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
