package parser

import (
	"strings"
	"testing"

	"github.com/wangkayu/reltitle/pkg/model"
	"github.com/wangkayu/reltitle/pkg/tokenize"
)

func decisionTokens(raw string) []model.Token {
	parts := tokenize.Tokenize(raw)
	out := make([]model.Token, 0, len(parts))
	for _, p := range parts {
		out = append(out, model.Token{Text: p})
	}
	return out
}

func hasCandidateKind(cands []model.EPEntity, kind string) bool {
	for _, c := range cands {
		if strings.EqualFold(strings.TrimSpace(string(c.Kind)), kind) {
			return true
		}
	}
	return false
}

func TestEPDecisionChoosePrimary_SuppressesSeasonWordFalseRange(t *testing.T) {
	tokens := decisionTokens("Season 2 - 01")
	cands := []model.EPEntity{
		{Kind: "RANGE", Axis: "SE", Season: "2", EpisodeRange: "2-1", TokenStart: 1, TokenEnd: 2, Source: "episode_range"},
		{Kind: "EP", Axis: "SE", Season: "2", Episode: "1", TokenStart: 3, TokenEnd: 4, Source: "episode"},
	}
	seasonHint := &epSeasonHint{Season: "2", TokenIdx: 0, Source: "season_keyword"}

	primary, kept, suppressed := choosePrimaryCandidate(tokens, cands, seasonHint)
	if primary == nil {
		t.Fatalf("primary should not be nil")
	}
	if got := strings.ToUpper(strings.TrimSpace(string(primary.Kind))); got != "EP" {
		t.Fatalf("primary.kind=%q want=%q", got, "EP")
	}
	if got := strings.TrimSpace(primary.Episode); got != "1" {
		t.Fatalf("primary.episode=%q want=%q", got, "1")
	}
	if hasCandidateKind(kept, "RANGE") {
		t.Fatalf("false range should be suppressed, kept=%+v", kept)
	}
	if len(suppressed) != 1 || suppressed[0].Reason != "season_episode_range_conflict" {
		t.Fatalf("suppressed=%+v want season conflict", suppressed)
	}
}

func TestEPDecisionChoosePrimary_SuppressesPartIndexFalseRange(t *testing.T) {
	tokens := decisionTokens("Part 3 - 25")
	cands := []model.EPEntity{
		{Kind: "RANGE", Axis: "ABS", EpisodeRange: "3-25", TokenStart: 1, TokenEnd: 2, Source: "episode_range"},
		{Kind: "EP", Axis: "ABS", Episode: "25", TokenStart: 3, TokenEnd: 4, Source: "episode"},
	}

	primary, kept, suppressed := choosePrimaryCandidate(tokens, cands, nil)
	if primary == nil {
		t.Fatalf("primary should not be nil")
	}
	if got := strings.ToUpper(strings.TrimSpace(string(primary.Kind))); got != "EP" {
		t.Fatalf("primary.kind=%q want=%q", got, "EP")
	}
	if got := strings.TrimSpace(primary.Episode); got != "25" {
		t.Fatalf("primary.episode=%q want=%q", got, "25")
	}
	if hasCandidateKind(kept, "RANGE") {
		t.Fatalf("false range should be suppressed, kept=%+v", kept)
	}
	if len(suppressed) != 1 || suppressed[0].Reason != "part_index_range_conflict" {
		t.Fatalf("suppressed=%+v want part conflict", suppressed)
	}
}

func TestEPDecisionChoosePrimary_SuppressesAliasSeasonEchoFalseRange(t *testing.T) {
	tokens := decisionTokens("一人之下 第6季 / The Outcast 6 - 14")
	cands := []model.EPEntity{
		{Kind: "RANGE", Axis: "SE", Season: "6", EpisodeRange: "6-14", TokenStart: 5, TokenEnd: 6, Source: "episode_range"},
		{Kind: "EP", Axis: "SE", Season: "6", Episode: "14", TokenStart: 7, TokenEnd: 8, Source: "ep_digit_token"},
	}
	seasonHint := &epSeasonHint{Season: "6", TokenIdx: 1, Source: "season_keyword"}

	primary, kept, suppressed := choosePrimaryCandidate(tokens, cands, seasonHint)
	if primary == nil {
		t.Fatalf("primary should not be nil")
	}
	if got := strings.ToUpper(strings.TrimSpace(string(primary.Kind))); got != "EP" {
		t.Fatalf("primary.kind=%q want=%q", got, "EP")
	}
	if got := strings.TrimSpace(primary.Episode); got != "14" {
		t.Fatalf("primary.episode=%q want=%q", got, "14")
	}
	if hasCandidateKind(kept, "RANGE") {
		t.Fatalf("false range should be suppressed, kept=%+v", kept)
	}
	if len(suppressed) != 1 || suppressed[0].Reason != "alias_season_echo_range_conflict" {
		t.Fatalf("suppressed=%+v want alias season echo conflict", suppressed)
	}
}

func TestEPDecisionChoosePrimary_KeepsTrueDelimitedRange(t *testing.T) {
	tokens := decisionTokens("01 ~ 20")
	cands := []model.EPEntity{
		{Kind: "RANGE", Axis: "ABS", EpisodeRange: "1-20", TokenStart: 0, TokenEnd: 1, Source: "episode_range"},
		{Kind: "EP", Axis: "ABS", Episode: "20", TokenStart: 2, TokenEnd: 3, Source: "episode"},
	}

	primary, kept, suppressed := choosePrimaryCandidate(tokens, cands, nil)
	if primary == nil {
		t.Fatalf("primary should not be nil")
	}
	if got := strings.ToUpper(strings.TrimSpace(string(primary.Kind))); got != "RANGE" {
		t.Fatalf("primary.kind=%q want=%q", got, "RANGE")
	}
	if got := strings.TrimSpace(primary.EpisodeRange); got != "1-20" {
		t.Fatalf("primary.episode_range=%q want=%q", got, "1-20")
	}
	if !hasCandidateKind(kept, "RANGE") {
		t.Fatalf("true range should be kept, kept=%+v", kept)
	}
	if len(suppressed) != 0 {
		t.Fatalf("suppressed=%+v want empty", suppressed)
	}
}

func TestEPDecisionChoosePrimary_KeepsSharedPrefixSeasonEpisodeRange(t *testing.T) {
	tokens := decisionTokens("S02E07 - 09")
	cands := []model.EPEntity{
		{Kind: "RANGE", Axis: "SE", Season: "2", EpisodeRange: "7-9", TokenStart: 0, TokenEnd: 1, Source: "episode_range"},
		{Kind: "EP", Axis: "SE", Season: "2", Episode: "9", TokenStart: 2, TokenEnd: 3, Source: "ep_digit_token"},
	}
	seasonHint := &epSeasonHint{Season: "2", TokenIdx: 0, Source: "sxxexx"}

	primary, kept, suppressed := choosePrimaryCandidate(tokens, cands, seasonHint)
	if primary == nil {
		t.Fatalf("primary should not be nil")
	}
	if got := strings.ToUpper(strings.TrimSpace(string(primary.Kind))); got != "RANGE" {
		t.Fatalf("primary.kind=%q want=%q", got, "RANGE")
	}
	if got := strings.TrimSpace(primary.EpisodeRange); got != "7-9" {
		t.Fatalf("primary.episode_range=%q want=%q", got, "7-9")
	}
	if !hasCandidateKind(kept, "RANGE") {
		t.Fatalf("shared-prefix range should be kept, kept=%+v", kept)
	}
	if len(suppressed) != 0 {
		t.Fatalf("suppressed=%+v want empty", suppressed)
	}
}

func TestSyncEPSlotWithCanonicalSignals_PreservesExtraCandidates(t *testing.T) {
	epslot := &model.EPSlot{
		Candidates: []model.EPEntity{
			{Kind: "SP", Axis: "NCED", SPKind: "NCED", Source: "sp_kind"},
		},
	}

	synced := syncEPSlotWithCanonicalSignals(epslot, "", "", "", "", "NCOP", -1, -1, -1)
	if synced == nil || synced.Primary == nil {
		t.Fatalf("synced primary should exist")
	}
	if got := strings.TrimSpace(synced.Primary.SPKind); got != "NCOP" {
		t.Fatalf("primary.sp_kind=%q want=%q", got, "NCOP")
	}
	hasNCED := false
	for _, c := range synced.Candidates {
		if strings.EqualFold(strings.TrimSpace(c.SPKind), "NCED") {
			hasNCED = true
			break
		}
	}
	if !hasNCED {
		t.Fatalf("extra candidate should be preserved, candidates=%+v", synced.Candidates)
	}
}
