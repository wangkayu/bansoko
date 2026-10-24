import { render, screen } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { SelectionOverrideFields } from "./SourcesFields";
import { t } from "../../lib/i18n";

it("unavailable group is not displayed as subscribed even with historical selection", () => {
  render(<SelectionOverrideFields origin="anibt" onChange={vi.fn()} value={{max_rss: 1, entries: [{provider: "anibt", group_ref: "retired", display_name: "Retired group", selection_mode: "auto", manual_selected: null, effective_selected: true, score: 90, count_in_limit: true}]}} serverSelection={[{provider: "anibt", group_ref: "retired", available: false, effective_selected: true, count_in_limit: true}]} />);
  expect(screen.getByText(t("forms.sources.unavailable"))).toBeInTheDocument();
  expect(screen.queryByText(t("anime.state.active"))).not.toBeInTheDocument();
});
