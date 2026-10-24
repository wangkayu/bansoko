import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useState } from "react";
import type { AnimeDraft, BusinessSnapshot, TagFilterRules } from "../../lib/api/client";
import { RulesTab } from "./RulesTab";
import type { DraftTabHost } from "./host";
import { t } from "../../lib/i18n";

// The pre-subscription draft's rules tab: the parser choice and the episode recognition rules are
// one block inside the "parsing" section, options from /parser-catalog (both regex and reltitle
// selectable), the value written into the draft business snapshot's parser envelope. Tag filtering
// is a preset rule, configurable before subscribing, written into the draft's
// rules.sections.tag_filters.

const parserCatalog = [
  { id: "regex", implementation_version: "regex-v1", output_contract_version: "episode-evidence-v1", display_name: "Regex rules", capabilities: ["release_title", "file_name", "decimal"], config_version: "regex-config-v1" },
  { id: "reltitle", implementation_version: "reltitle@x", output_contract_version: "episode-evidence-v1", display_name: "reltitle", capabilities: ["release_title", "file_name", "decimal"], config_version: "lexicon:x" },
];

const PATTERN = "\\s-\\s*0*([0-9]{1,3})";

const draftTagFilters: TagFilterRules = {
  anibt: { require: [], exclude: ["language:CHT"] },
  title: { require: [], exclude: [] },
};

const loadedParser: BusinessSnapshot["parser"] = {
  kind: "regex",
  policy_version: 2,
  profile_version: 0,
  provenance: "frozen_at_candidate_save",
  rules_owner_version: 0,
  schema_version: "parser-policy-envelope-v1",
  source_owner: "global_parser_policy_default",
};

function draftFixture(): AnimeDraft {
  return {
    origin: "mikan",
    origin_entry_ref: "4111",
    title: "番剧",
    title_override: null,
    instance_label: "番剧",
    metadata: { bgm: null, tmdb: null, total_episodes: null },
    subgroups: { max_rss: 3, entries: [] },
    rss_sources: [],
    rules: {
      sections: {
        parser: { mode: "replace", value: { episode_patterns: [PATTERN] } },
        release_filter: { mode: "replace", value: { must_contain: "", not_in: "" } },
        file_filter: { mode: "replace", value: { must_contain: "", not_in: "" } },
        naming: { mode: "replace", value: { language: "js-naming-1", unknown: { directory_template: "", video_filename_template: "" }, tv: { directory_template: "", video_filename_template: "" }, movie: { directory_template: "", video_filename_template: "" }, subtitle_suffix_template: "" } },
        file_effect: { mode: "replace", value: { operation: "copy", collision_policy: "fail" } },
        tag_filters: { mode: "replace", value: structuredClone(draftTagFilters) },
      },
    },
    business: { parser: structuredClone(loadedParser), unclassified_route: "skip" } as unknown as BusinessSnapshot,
  } as unknown as AnimeDraft;
}

let latest: AnimeDraft | undefined;

function Harness({ origin = "mikan" }: { origin?: AnimeDraft["origin"] }) {
  const [draft, setDraft] = useState<AnimeDraft>(() => ({ ...draftFixture(), origin }));
  latest = draft;
  const host: DraftTabHost = {
    mode: "draft",
    draft,
    onDraftChange: setDraft,
    previewable: true,
    feedSnapshots: [],
    onFeedSnapshotsChange: () => {},
    observedGroups: [],
    onDiscovered: () => {},
  };
  return <RulesTab host={host} />;
}

function renderRules(origin?: AnimeDraft["origin"]) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <Harness origin={origin} />
    </QueryClientProvider>,
  );
}

function tagFilterCard() {
  return within(screen.getByText(t("tagFilters.title")).closest("section")!);
}

beforeEach(() => {
  latest = undefined;
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    if (String(input).includes("/parser-catalog")) {
      return new Response(JSON.stringify(parserCatalog), { status: 200, headers: { "Content-Type": "application/json" } });
    }
    return new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } });
  }));
});

afterEach(() => vi.unstubAllGlobals());

describe("订阅前规则 tab：解析器与集数识别规则同一块", () => {
  it("reltitle 可选；选中后写入草稿快照的解析器信封并隐藏识别规则，选回正则恢复载入的信封", async () => {
    const user = userEvent.setup();
    renderRules();
    const group = await screen.findByRole("radiogroup", { name: t("policy.parser.kindLabel") });
    const reltitle = await screen.findByRole("radio", { name: new RegExp(t("policy.parser.kindReltitle")) });
    expect(reltitle).toBeEnabled();
    // The parser choice sits in the "parsing" section with the recognition rules right below.
    const section = group.closest("section");
    expect(section).not.toBeNull();
    expect(section!.textContent).toContain(t("settings.rules.episodePatterns"));

    await user.click(reltitle);
    expect(latest?.business?.parser.kind).toBe("reltitle");
    expect(latest?.business?.parser.source_owner).toBe("candidate_draft");
    expect(screen.queryByText(t("settings.rules.episodePatterns"))).toBeNull();
    expect(screen.getByText(t("forms.rules.episodePatternsUnused"))).toBeVisible();
    // The recognition rules themselves are never cleared: switching back to regex finds them intact.
    expect(latest?.rules.sections.parser).toEqual({ mode: "replace", value: { episode_patterns: [PATTERN] } });

    await user.click(screen.getByRole("radio", { name: new RegExp(t("policy.parser.kindRegex")) }));
    expect(latest?.business?.parser).toEqual(loadedParser);
    expect(screen.getByText(t("settings.rules.episodePatterns"))).toBeVisible();
  });

  it("规则 tab 末尾不再有单独的解析器下拉", async () => {
    renderRules();
    await screen.findByRole("radiogroup", { name: t("policy.parser.kindLabel") });
    expect(screen.queryByRole("combobox", { name: t("policy.parser.kindLabel") })).toBeNull();
    expect(screen.getAllByRole("radiogroup", { name: t("policy.parser.kindLabel") })).toHaveLength(1);
  });
});

describe("订阅前规则 tab：标签过滤", () => {
  it("显示草稿自己的标签过滤，没有使用默认 / 自定义切换", async () => {
    renderRules();
    await screen.findByRole("radiogroup", { name: t("policy.parser.kindLabel") });
    expect(latest?.rules.sections.tag_filters).toEqual({ mode: "replace", value: draftTagFilters });
    // A Mikan draft has no AniBT source: the AniBT row does not display.
    expect(tagFilterCard().queryByText(t("anibtTags.title"))).toBeNull();
    expect(tagFilterCard().queryByRole("radio", { name: t("config.useDefaultsConfirm") })).toBeNull();
    // Every other rules section stays untouched.
    expect(latest?.rules.sections.parser).toEqual({ mode: "replace", value: { episode_patterns: [PATTERN] } });
  });

  it("AniBT 草稿显示 AniBT 行", async () => {
    renderRules("anibt");
    await screen.findByRole("radiogroup", { name: t("policy.parser.kindLabel") });
    expect(tagFilterCard().getByText(t("anibtTags.title"))).toBeVisible();
  });
});
