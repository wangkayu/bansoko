package parser

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	reltitlemodel "github.com/wangkayu/reltitle/pkg/model"

	"bansoko/internal/episodeidentity"
	"bansoko/internal/parser/reltitletags"
)

// ReltitleAdapter is the vendored reltitle parser as an episode source. It
// normalizes reltitle's episode conclusion into the same typed evidence the
// regex adapter returns: a plain episode is a value; ranges, lists, seasons
// and specials are typed unsupported evidence and fail closed downstream.
// Offset, final episode and target path are never produced here.
type ReltitleAdapter struct{}

func (ReltitleAdapter) Descriptor() ParserDescriptor {
	return ParserDescriptor{
		ID: ParserReltitle, ImplementationVersion: reltitletags.ImplementationVersion,
		OutputContractVersion: "episode-evidence-v1", DisplayName: "reltitle",
		Capabilities:  []string{"release_title", "file_name", "decimal"},
		ConfigVersion: reltitletags.ConfigDigest(),
	}
}

// ValidateConfig accepts no user configuration: the parser's behavior is the
// pinned lexicon, so episode patterns cannot be combined with it.
func (ReltitleAdapter) ValidateConfig(config json.RawMessage) error {
	trimmed := strings.TrimSpace(string(config))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	return errors.New("reltitle takes no configuration")
}

func (ReltitleAdapter) ParseDraft(_ context.Context, input EpisodeParseInput, _ json.RawMessage) (EpisodeEvidenceDraft, error) {
	title := strings.TrimSpace(input.Text)
	if title == "" {
		return EpisodeEvidenceDraft{Status: StatusMissing, Shape: ShapeUnknown}, nil
	}
	if err := reltitletags.Ready(); err != nil {
		return EpisodeEvidenceDraft{}, err
	}
	result, err := reltitletags.Parse(title)
	if err != nil {
		// An oversized or unparseable title is typed evidence, not a pipeline
		// fault; the release simply has no usable episode.
		return EpisodeEvidenceDraft{Status: StatusInvalid, Shape: ShapeUnknown}, nil
	}
	if result.EPSemantics == nil || result.EPSemantics.Primary == nil {
		return EpisodeEvidenceDraft{Status: StatusMissing, Shape: ShapeUnknown}, nil
	}
	return reltitleDraft(result.EPSemantics.Primary), nil
}

func reltitleDraft(primary *reltitlemodel.EPReference) EpisodeEvidenceDraft {
	switch primary.Kind.Canonical() {
	case reltitlemodel.EPKindEP:
		if axis := primary.Axis.Canonical(); axis == reltitlemodel.EPAxisSP || axis == reltitlemodel.EPAxisOVA {
			return EpisodeEvidenceDraft{Status: StatusUnsupported, Shape: ShapeSpecial, RawCapture: primary.Episode}
		}
		raw := strings.TrimSpace(primary.Episode)
		if _, err := episodeidentity.CanonicalEpisode(raw); err != nil {
			return EpisodeEvidenceDraft{Status: StatusInvalid, Shape: ShapeSingle, RawCapture: raw}
		}
		draft := EpisodeEvidenceDraft{Status: StatusValue, Shape: ShapeSingle, RawCapture: raw, Episode: raw}
		if season, ok := reltitleSeason(primary.Season); ok {
			draft.Season = &season
		}
		return draft
	case reltitlemodel.EPKindEPList:
		return EpisodeEvidenceDraft{Status: StatusUnsupported, Shape: ShapeList, RawCapture: strings.Join(primary.EpisodeList, ",")}
	case reltitlemodel.EPKindRange:
		return EpisodeEvidenceDraft{Status: StatusUnsupported, Shape: ShapeRange, RawCapture: primary.RangeStart + "-" + primary.RangeEnd}
	case reltitlemodel.EPKindSeason, reltitlemodel.EPKindSeasonRange:
		return EpisodeEvidenceDraft{Status: StatusUnsupported, Shape: ShapeSeasonOnly, RawCapture: firstNonEmpty(primary.SeasonRange, primary.Season)}
	case reltitlemodel.EPKindSP:
		return EpisodeEvidenceDraft{Status: StatusUnsupported, Shape: ShapeSpecial, RawCapture: firstNonEmpty(primary.SPKind, primary.Episode)}
	default:
		return EpisodeEvidenceDraft{Status: StatusMissing, Shape: ShapeUnknown}
	}
}

func reltitleSeason(raw string) (int, bool) {
	value, err := strconv.Atoi(strings.TrimLeft(strings.TrimSpace(strings.ToUpper(raw)), "S"))
	if err != nil || value < 0 || value > 99 {
		return 0, false
	}
	return value, true
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
