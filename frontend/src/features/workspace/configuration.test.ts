import { describe, expect, it } from "vitest";
import type { AnimeWorkspace, AnimeWorkspaceConfiguration } from "../../lib/api/client";
import { countTags } from "../../lib/titleTags";
import { observedExtraTags } from "../../lib/anibtTags";
import { workspaceConfiguration, workspaceReleaseSummary, workspaceStable } from "./configuration";

function history(): AnimeWorkspace {
  return {
    rss_sources: [{ id: "source", source_kind: "custom_rss" }],
    rss_source_runtimes: [{ rss_source_id: "source", health: "ready" }],
    subgroups: { effective_selection: [] },
    download_tasks: [], artifacts: [], file_operations: [],
    releases: Array.from({ length: 512 }, (_, index) => ({
      id: `release-${index}`, rss_source_id: "source", verdict: index === 511 ? "discovered" : "filtered",
      raw_facts: { published_at: index === 0 ? "2026-11-10T09:00:00+08:00" : index === 1 ? "2026-11-10T02:00:00Z" : undefined },
      source_facts: {
        anibt: { tags: index % 2 ? ["tag:EXTRA", "tag:extra", "tag:EXTRA"] : [] },
        title_tags: { tags: index % 2 ? ["codec:H265", "codec:h265"] : [] },
      },
    })),
  } as unknown as AnimeWorkspace;
}

describe("configuration workspace projection", () => {
  it("retains all-history summary and tag counting while omitting execution arrays", () => {
    const full = history();
    const light = workspaceConfiguration(full);
    expect(light.release_summary.count).toBe(512);
    expect(light.release_summary.latest_published_at).toBe("2026-11-10T09:00:00+08:00");
    expect(workspaceStable(light)).toBe(false); // the oldest release still participates
    expect(workspaceReleaseSummary(light)).toBe(light.release_summary);
    expect(light).not.toHaveProperty("releases");
    expect(light).not.toHaveProperty("download_tasks");
    expect(light).not.toHaveProperty("artifacts");
    expect(light).not.toHaveProperty("file_operations");
    const before = full.releases.map(release => release.source_facts.anibt?.tags);
    const after = light.release_summary.tag_observations.map(tags => tags.anibt_tags);
    expect(countTags(after)).toEqual(countTags(before));
    expect(observedExtraTags(after)).toEqual(observedExtraTags(before));
    expect(countTags(light.release_summary.tag_observations.map(tags => tags.title_tags)))
      .toEqual(countTags(full.releases.map(release => release.source_facts.title_tags?.tags)));
    full.releases[511]!.verdict = "filtered";
    expect(workspaceStable(full)).toBe(true);
  });

  it("reads a server summary without requiring or inventing history", () => {
    const light = { release_summary: { count: 900, owner_chain_stable: true, tag_observations: [] } } as unknown as AnimeWorkspaceConfiguration;
    expect(workspaceStable(light)).toBe(true);
    expect(workspaceReleaseSummary(light).count).toBe(900);
    expect(workspaceReleaseSummary(light).latest_published_at).toBeUndefined();
  });
});
