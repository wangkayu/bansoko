import { fireEvent, render, screen, within } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import type { SubgroupScoreEntry, SubgroupsSection } from "../../lib/api/client";
import { SelectionOverrideFields, type ServerSelectionFact } from "./SourcesFields";
import { t } from "../../lib/i18n";

// One subscription belongs to one provider: the fixtures are all anibt entries matching
// origin="anibt". The main view is the direct row list (group name/selection policy/count) with
// no "manage" dialog anymore; scores are read-only. Global settings own scoring and ordering,
// while a single anime owns only its manual selection policy and count switches.
const entries: SubgroupScoreEntry[] = [
  {
    provider: "anibt",
    group_ref: "ranked-ref",
    display_name: "Ranked 组",
    selection_mode: "auto",
    manual_selected: null,
    effective_selected: true,
    score: 88,
  },
  {
    provider: "anibt",
    group_ref: "always-ref",
    display_name: "Always 组",
    selection_mode: "manual",
    manual_selected: true,
    effective_selected: true,
    score: 70,
  },
  {
    provider: "anibt",
    group_ref: "never-ref",
    display_name: "Never 组",
    selection_mode: "manual",
    manual_selected: false,
    effective_selected: false,
    score: 60,
  },
];

const initialValue: SubgroupsSection = { max_rss: 3, entries };

function ControlledFields({
  initial = initialValue,
  origin = "anibt",
  onChange,
  onGroupAction,
  serverSelection,
}: {
  initial?: SubgroupsSection;
  origin?: "anibt" | "mikan" | "custom";
  onChange?: (next: SubgroupsSection) => void;
  onGroupAction?: Parameters<typeof SelectionOverrideFields>[0]["onGroupAction"];
  serverSelection?: readonly ServerSelectionFact[];
}) {
  const [value, setValue] = useState(initial);
  return (
    <SelectionOverrideFields
      value={value}
      onChange={(next) => {
        setValue(next);
        onChange?.(next);
      }}
      origin={origin}
      onGroupAction={onGroupAction}
      serverSelection={serverSelection}
    />
  );
}

/** The server's selection facts (from the read model before subscribing, from the workspace after; the same shape). */
function selectionFacts(
  facts: ReadonlyArray<{
    provider?: "anibt" | "mikan";
    group_ref: string;
    effective_selected: boolean;
    reason_code?: string | null;
    score_breakdown?: ServerSelectionFact["score_breakdown"];
  }>,
): ServerSelectionFact[] {
  return facts.map((fact) => ({ provider: fact.provider ?? "anibt", ...fact }));
}

describe("SelectionOverrideFields（直接行列表）", () => {
  it("主视图直接展示组行，无需先点管理进弹窗", () => {
    render(<ControlledFields />);

    expect(screen.getByText("Ranked 组")).toBeInTheDocument();
    expect(screen.getByText("Always 组")).toBeInTheDocument();
    expect(screen.getByText("Never 组")).toBeInTheDocument();
    // No manage dialog entry and no provider partition rows anymore: row content sits in the main view, no dialog to open first
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.queryByText("AniBT")).not.toBeInTheDocument();
    expect(screen.queryByText("Mikan")).not.toBeInTheDocument();
  });

  it("组行按积分降序排列，积分只读展示", () => {
    render(<ControlledFields />);
    const list = screen.getByRole("list", { name: t("forms.sources.listAria") });
    const names = within(list)
      .getAllByRole("listitem")
      .map((row) => row.textContent ?? "");
    expect(names[0]).toContain("Ranked 组");
    expect(names[1]).toContain("Always 组");
    expect(names[2]).toContain("Never 组");
    expect(within(list).getByText(t("anime.sources.score", { n: "88" }))).toBeInTheDocument();
  });

  it("排序：已选置顶、排除置底，其余按分数降序，同分按名称", () => {
    const rows: SubgroupScoreEntry[] = [
      { ...entries[2], group_ref: "never-high", display_name: "排除高分", score: 99 },
      { ...entries[0], group_ref: "auto-b", display_name: "B 组", effective_selected: false, score: 50 },
      { ...entries[0], group_ref: "auto-a", display_name: "A 组", effective_selected: false, score: 50 },
      { ...entries[0], group_ref: "auto-top", display_name: "高分未选", effective_selected: false, score: 80 },
      { ...entries[0], group_ref: "picked-low", display_name: "已选低分", score: 10 },
    ];
    render(
      <ControlledFields
        initial={{ max_rss: 3, entries: rows }}
        serverSelection={[
          { provider: "anibt", group_ref: "never-high", effective_selected: false },
          { provider: "anibt", group_ref: "auto-b", effective_selected: false },
          { provider: "anibt", group_ref: "auto-a", effective_selected: false },
          { provider: "anibt", group_ref: "auto-top", effective_selected: false },
          { provider: "anibt", group_ref: "picked-low", effective_selected: true },
        ]}
      />,
    );
    const list = screen.getByRole("list", { name: t("forms.sources.listAria") });
    const names = within(list).getAllByRole("listitem").map((row) => row.textContent ?? "");
    const order = ["已选低分", "高分未选", "A 组", "B 组", "排除高分"];
    order.forEach((name, index) => expect(names[index]).toContain(name));
  });

  it("没有单番评分编辑控件", () => {
    render(<ControlledFields />);
    expect(screen.queryByLabelText(/评分$/)).not.toBeInTheDocument();
    expect(screen.queryByLabelText("默认分数")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: t("settings.provider.rules.title") })).not.toBeInTheDocument();
  });

  // Selection facts come only from the latest server read model; with no result nothing may be
  // guessed. An excluded row's subscription state is meaningless and never rendered as a badge
  // (existing behavior).
  it("三态徽标与服务端一致：真→订阅中，假→未订阅，缺事实/无读模型→未预览", () => {
    const { unmount } = render(<ControlledFields />);
    expect(screen.queryByText(t("anime.sources.selected"))).not.toBeInTheDocument();
    expect(screen.queryByText(t("anime.sources.unselected"))).not.toBeInTheDocument();
    expect(screen.getAllByText(t("forms.sources.notPreviewed"))).toHaveLength(3);
    expect(
      screen.getByText((_, el) => el?.tagName === "SPAN" && el.textContent === `${t("forms.sources.summary")} ${t("forms.sources.notPreviewed")}`),
    ).toBeInTheDocument();
    unmount();

    render(
      <ControlledFields
        serverSelection={selectionFacts([
          { group_ref: "ranked-ref", effective_selected: true, reason_code: "ranked_within_max_rss" },
          { group_ref: "always-ref", effective_selected: false },
          { group_ref: "never-ref", effective_selected: true, reason_code: "policy_always" },
        ])}
      />,
    );
    const rows = within(screen.getByRole("list", { name: t("forms.sources.listAria") })).getAllByRole("listitem");
    expect(rows[0]).toHaveTextContent(t("anime.sources.selected"));
    expect(rows[1]).toHaveTextContent(t("anime.sources.unselected"));
    // An excluded row has no badge: even a true server fact does not show subscribed.
    expect(rows[2]).not.toHaveTextContent(t("anime.sources.selected"));
    expect(rows[2]).not.toHaveTextContent(t("anime.sources.unselected"));
    expect(
      screen.getByText((_, el) => el?.tagName === "SPAN" && el.textContent === `${t("forms.sources.summary")} 1${t("forms.sources.outOf", { n: 3 })}${t("forms.sources.notCounted", { n: 1 })}`),
    ).toBeInTheDocument();
  });

  // The not-subscribed reason comes from the backend decision and the frontend only displays it. Without a reason code nothing may be guessed from booleans.
  it("未订阅行的提示来自服务端 reason_code，缺原因码就不给提示", () => {
    const reason = "max_rss_exhausted";
    const { unmount } = render(
      <ControlledFields
        serverSelection={selectionFacts([
          { group_ref: "ranked-ref", effective_selected: true },
          { group_ref: "always-ref", effective_selected: false, reason_code: reason },
        ])}
      />,
    );
    const rows = within(screen.getByRole("list", { name: t("forms.sources.listAria") })).getAllByRole("listitem");
    expect(within(rows[1]).getByText(t("anime.sources.unselected"))).toHaveAttribute("title", t(`reason.${reason}`));
    // A selected row never explains "why not subscribed", so there is no tip.
    expect(within(rows[0]).getByText(t("anime.sources.selected"))).not.toHaveAttribute("title");
    unmount();

    // An unregistered reason code goes into the tooltip as-is instead of being swallowed into a generic "unknown reason".
    render(
      <ControlledFields
        serverSelection={selectionFacts([
          { group_ref: "ranked-ref", effective_selected: true },
          { group_ref: "always-ref", effective_selected: false, reason_code: "brand_new_code" },
        ])}
      />,
    );
    const unknownRows = within(screen.getByRole("list", { name: t("forms.sources.listAria") })).getAllByRole("listitem");
    expect(within(unknownRows[1]).getByText(t("anime.sources.unselected"))).toHaveAttribute("title", "brand_new_code");
  });

  it("名额计数只认服务端原因码，必选等其他原因的选中不占名额", () => {
    // In the draft, ranked-ref is auto+counted; the server's fact is that it is selected but not
    // counted. Another provider's same-named group's facts must not pollute this provider's rows
    // (otherwise always-ref would be translated into subscribed).
    render(
      <ControlledFields
        serverSelection={selectionFacts([
          { group_ref: "ranked-ref", effective_selected: true, reason_code: "policy_always" },
          { group_ref: "always-ref", effective_selected: false },
          { provider: "mikan", group_ref: "always-ref", effective_selected: true },
        ])}
      />,
    );
    const rows = within(screen.getByRole("list", { name: t("forms.sources.listAria") })).getAllByRole("listitem");
    expect(rows[0]).toHaveTextContent(t("anime.sources.selected"));
    expect(rows[1]).toHaveTextContent(t("anime.sources.unselected"));
    expect(
      screen.getByText((_, el) => el?.tagName === "SPAN" && el.textContent === `${t("forms.sources.summary")} 0${t("forms.sources.outOf", { n: 3 })}${t("forms.sources.notCounted", { n: 1 })}`),
    ).toBeInTheDocument();
  });

  it("读模型里没有该组的事实时该行显示未预览，其余行仍按服务端事实", () => {
    render(
      <ControlledFields
        serverSelection={selectionFacts([
          { group_ref: "ranked-ref", effective_selected: true },
        ])}
      />,
    );
    const rows = within(screen.getByRole("list", { name: t("forms.sources.listAria") })).getAllByRole("listitem");
    expect(rows[0]).toHaveTextContent(t("anime.sources.selected"));
    expect(rows[1]).toHaveTextContent(t("forms.sources.notPreviewed"));
  });

  it("订阅数量弹窗合法值点「应用」写入 max_rss", () => {
    const onChange = vi.fn();
    render(<ControlledFields onChange={onChange} />);

    fireEvent.click(screen.getByRole("button", { name: t("forms.sources.maxButton") }));
    const dialog = screen.getByRole("dialog");
    const input = within(dialog).getByLabelText(t("forms.sources.maxField"));
    fireEvent.change(input, { target: { value: "5" } });
    // The dialog confirms "apply" (writing the draft), not "save" — saving lives only in the footer.
    expect(within(dialog).queryByRole("button", { name: t("act.save") })).not.toBeInTheDocument();
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.sources.maxApply") }));

    expect(onChange).toHaveBeenLastCalledWith({ max_rss: 5, entries });
  });

  it("订阅数量弹窗非法输入报错并保持原值，不静默归零", () => {
    const onChange = vi.fn();
    render(<ControlledFields onChange={onChange} />);

    fireEvent.click(screen.getByRole("button", { name: t("forms.sources.maxButton") }));
    const dialog = screen.getByRole("dialog");
    const input = within(dialog).getByLabelText(t("forms.sources.maxField"));
    fireEvent.change(input, { target: { value: "abc" } });

    expect(within(dialog).getByRole("alert")).toHaveTextContent(t("settings.policy.maxRssInvalid"));
    expect(within(dialog).getByRole("button", { name: t("forms.sources.maxApply") })).toBeDisabled();
    expect(onChange).not.toHaveBeenCalled();
  });

  it("行内切换策略并保留完整条目（含 provider_defaults/name_keywords 原样透传）", () => {
    const onChange = vi.fn();
    const withScoring: SubgroupsSection = {
      ...initialValue,
      provider_defaults: { anibt: { mode: "blacklist", score: 50, groups: [] } },
      name_keywords: { anibt: [{ keyword: "简繁", score: -30 }] },
    };
    render(<ControlledFields initial={withScoring} onChange={onChange} />);

    const rankedGroup = screen.getByRole("radiogroup", { name: t("forms.sources.policyAria", { name: "Ranked 组" }) });
    fireEvent.click(within(rankedGroup).getByRole("radio", { name: t("forms.sources.policyAlways") }));

    expect(onChange).toHaveBeenLastCalledWith({
      ...withScoring,
      entries: [
        { ...entries[0], selection_mode: "manual", manual_selected: true },
        entries[1],
        entries[2],
      ],
    });
  });

  it("策略控件是紧凑尺寸：自动选中时安静，必选/排除选中时着色", () => {
    render(<ControlledFields onChange={vi.fn()} />);
    const group = screen.getByRole("radiogroup", { name: t("forms.sources.policyAria", { name: "Ranked 组" }) });
    expect(group).toHaveClass("tw:p-0.5");
    expect(within(group).getByRole("radio", { name: t("forms.sources.policyAuto") })).toHaveClass("tw:data-checked:bg-card");
    expect(within(group).getByRole("radio", { name: t("forms.sources.policyAlways") })).toHaveClass("tw:data-checked:bg-primary/15");
    expect(within(group).getByRole("radio", { name: t("forms.sources.policyNever") })).toHaveClass("tw:data-checked:text-destructive");
  });

  it("没有手工添加入口：组列表只来自发现，用户只控制通行", () => {
    render(<ControlledFields />);
    expect(screen.queryByLabelText("手工添加字幕组")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "添加" })).not.toBeInTheDocument();
  });

  it("entries 为空时展示提示", () => {
    render(
      <SelectionOverrideFields value={{ max_rss: 0, entries: [] }} onChange={() => {}} origin="mikan" />,
    );
    expect(screen.getByText(new RegExp(t("forms.sources.noGroups")))).toBeInTheDocument();
    expect(screen.queryByRole("list")).not.toBeInTheDocument();
  });

  it("custom 来源展示固定字幕组提示", () => {
    render(
      <SelectionOverrideFields
        value={{ max_rss: 0, entries: [] }}
        onChange={() => {}}
        origin="custom"
        fixedGroupName="固定组"
      />,
    );
    expect(screen.getByText("固定组")).toBeInTheDocument();
    expect(screen.queryByRole("list")).not.toBeInTheDocument();
  });

  // GroupActions: policy switches fire restore_auto / force_include /
  // force_exclude for the workspace owner to collect into SubgroupsPatch.
  it("策略切换触发对应 GroupAction（ranked→restore_auto, always→force_include, never→force_exclude）", () => {
    const onGroupAction = vi.fn();
    render(<ControlledFields onGroupAction={onGroupAction} />);

    fireEvent.click(
      within(screen.getByRole("radiogroup", { name: t("forms.sources.policyAria", { name: "Ranked 组" }) })).getByRole("radio", {
        name: t("forms.sources.policyAlways"),
      }),
    );
    expect(onGroupAction).toHaveBeenLastCalledWith({
      provider: "anibt",
      group_ref: "ranked-ref",
      action: "force_include",
    });

    fireEvent.click(
      within(screen.getByRole("radiogroup", { name: t("forms.sources.policyAria", { name: "Always 组" }) })).getByRole("radio", {
        name: t("forms.sources.policyAuto"),
      }),
    );
    expect(onGroupAction).toHaveBeenLastCalledWith({
      provider: "anibt",
      group_ref: "always-ref",
      action: "restore_auto",
    });

    // Radio semantics: re-pressing the option that is already selected is not a
    // change, so the never mapping is exercised on a row that is not "never" yet.
    fireEvent.click(
      within(screen.getByRole("radiogroup", { name: t("forms.sources.policyAria", { name: "Ranked 组" }) })).getByRole("radio", {
        name: t("forms.sources.policyNever"),
      }),
    );
    expect(onGroupAction).toHaveBeenLastCalledWith({
      provider: "anibt",
      group_ref: "ranked-ref",
      action: "force_exclude",
    });
  });
});

describe("SelectionOverrideFields · 草稿发现观察行", () => {
  const observed = [
    { provider: "anibt" as const, group_ref: "obs-ref", display_name: "观察组", feed_url: "https://example.test/feed", observed_at: "2026-09-27T00:00:00Z" },
  ];
  const empty: SubgroupsSection = { max_rss: 3, entries: [] };

  function ControlledObserved({ onChange }: { onChange: (next: SubgroupsSection) => void }) {
    const [value, setValue] = useState(empty);
    return (
      <SelectionOverrideFields
        value={value}
        origin="anibt"
        observed={observed}
        onChange={(next) => {
          setValue(next);
          onChange(next);
        }}
      />
    );
  }

  it("发现的组显示为默认评分观察行，但不写入 entries", () => {
    const onChange = vi.fn();
    render(<ControlledObserved onChange={onChange} />);
    const row = screen.getByText("观察组").closest("li")!;
    expect(within(row).getByText(t("anime.sources.defaultScore"))).toBeInTheDocument();
    expect(within(row).queryByRole("checkbox")).not.toBeInTheDocument();
    expect(within(row).getByRole("radiogroup", { name: t("forms.sources.policyAria", { name: "观察组" }) })).toBeInTheDocument();
    expect(screen.queryByText(new RegExp(t("forms.sources.noGroups")))).not.toBeInTheDocument();
    expect(onChange).not.toHaveBeenCalled();
  });

  it("显式必选产生 manual entry，改回自动删除该 entry 而不是制造积分覆盖", () => {
    const onChange = vi.fn();
    render(<ControlledObserved onChange={onChange} />);
    const policy = () => screen.getByRole("radiogroup", { name: t("forms.sources.policyAria", { name: "观察组" }) });

    fireEvent.click(within(policy()).getByRole("radio", { name: t("forms.sources.policyAlways") }));
    expect(onChange).toHaveBeenLastCalledWith({
      max_rss: 3,
      entries: [expect.objectContaining({ group_ref: "obs-ref", selection_mode: "manual", manual_selected: true })],
    });

    fireEvent.click(within(policy()).getByRole("radio", { name: t("forms.sources.policyAuto") }));
    expect(onChange).toHaveBeenLastCalledWith({ max_rss: 3, entries: [] });
  });

  // The score composition comes only from the server decision: final score, the site's default
//   score, keywords and the total shown item by item, list exclusions naming which site's
//   blacklist, and reasons like slots full shown as chips.
  it("每组显示服务端给出的最终分、评分构成与原因", () => {
    render(
      <ControlledFields
        serverSelection={selectionFacts([
          {
            group_ref: "ranked-ref", effective_selected: true, reason_code: "ranked_within_max_rss",
            score_breakdown: { site_mode: "blacklist", site_default_score: 50, list_effect: "none", keywords: [{ keyword: "简繁", score: 20 }], entry_override: "none", final_score: 70 },
          },
          {
            group_ref: "always-ref", effective_selected: false, reason_code: "site_blacklisted",
            score_breakdown: { site_mode: "blacklist", site_default_score: 50, list_effect: "blacklisted", keywords: [], entry_override: "none", final_score: null },
          },
          {
            group_ref: "never-ref", effective_selected: false, reason_code: "max_rss_exhausted",
            score_breakdown: { site_mode: "blacklist", site_default_score: 50, list_effect: "none", keywords: [], entry_override: "none", final_score: 50 },
          },
        ])}
      />,
    );
    const rows = within(screen.getByRole("list", { name: t("forms.sources.listAria") })).getAllByRole("listitem");
    const byName = (name: string) => rows.find((row) => row.textContent?.includes(name))!;
    const ranked = byName("Ranked 组");
    expect(within(ranked).getByText(t("anime.sources.finalScore", { n: 70 }))).toBeInTheDocument();
    expect(ranked.textContent).toContain("站点默认 50 · 关键词「简繁」+20 · 合计 70 · 按评分进入订阅名额");
    const blacklisted = byName("Always 组");
    expect(within(blacklisted).getByText(t("anime.sources.noFinalScore"))).toBeInTheDocument();
    expect(within(blacklisted).getByText("AniBT 黑名单排除")).toBeInTheDocument();
    expect(blacklisted.textContent).not.toContain(t("reason.site_blacklisted"));
    expect(byName("Never 组").textContent).toContain("合计 50 · 名额已满，评分低于已选字幕组");
    // With a final score present, ordering follows the final score.
    expect(rows[0].textContent).toContain("Ranked 组");
  });
});
