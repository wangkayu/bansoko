package parser

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestParseEpisodeNormalizesRegexDecimalAndDigest(t *testing.T) {
	registry := NewDefaultRegistry()
	result, err := ParseEpisode(context.Background(), registry,
		ParserSelection{ID: ParserRegex},
		EpisodeParseInput{Kind: InputFileName, Text: "[Group] Show - 07.50 [1080p]"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusValue || result.Shape != ShapeSingle || result.Episode == nil || *result.Episode != "7.5" {
		t.Fatalf("unexpected normalized result: %+v", result)
	}
	if !strings.HasPrefix(result.InputDigest, "sha256:") || !strings.HasPrefix(result.ResultDigest, "sha256:") {
		t.Fatalf("missing deterministic digests: %+v", result)
	}
	other, err := ParseEpisode(context.Background(), registry,
		ParserSelection{ID: ParserRegex},
		EpisodeParseInput{Kind: InputFileName, Text: "[Group] Show - 07.50 [1080p]"})
	if err != nil || other.ResultDigest != result.ResultDigest {
		t.Fatalf("same input must produce same result digest: %+v / %+v", result, other)
	}
}

func TestParseEpisodeDoesNotFallbackAfterSelectedParserFailure(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(rejectingAdapter{}); err != nil {
		t.Fatal(err)
	}
	_, err := ParseEpisode(context.Background(), registry, ParserSelection{ID: "rejecting"}, EpisodeParseInput{Kind: InputFileName, Text: "Show - 01"})
	if err == nil || !strings.Contains(err.Error(), "selected parser failed") {
		t.Fatalf("selected parser failure must be returned directly: %v", err)
	}
}

func TestNormalizerRejectsParserPolicyLeak(t *testing.T) {
	descriptor := ParserDescriptor{ID: "test", ImplementationVersion: "v1", OutputContractVersion: "evidence-v1"}
	_, err := Normalize(descriptor, nil, EpisodeParseInput{Kind: InputFileName, Text: "Show - 01"}, EpisodeEvidenceDraft{
		Status: StatusValue, Shape: ShapeSingle, RawCapture: "01", Episode: "1", Offset: intPtr(2),
	})
	var violation *ParserContractError
	if !asContractError(err, &violation) {
		t.Fatalf("expected parser contract violation, got %v", err)
	}
}

func TestNormalizerRejectsUnknownInputKind(t *testing.T) {
	descriptor := ParserDescriptor{ID: "test", ImplementationVersion: "v1", OutputContractVersion: "evidence-v1"}
	_, err := Normalize(descriptor, nil, EpisodeParseInput{Kind: InputKind("filesystem_path"), Text: "Show - 01"}, EpisodeEvidenceDraft{
		Status: StatusValue, Shape: ShapeSingle, RawCapture: "01", Episode: "1",
	})
	if err == nil || !strings.Contains(err.Error(), "input kind") {
		t.Fatalf("unknown input kind must be rejected: %v", err)
	}
}

func TestNormalizeRejectsInvalidSpanAndShapeCombination(t *testing.T) {
	descriptor := ParserDescriptor{ID: "test", ImplementationVersion: "v1", OutputContractVersion: "evidence-v1"}
	_, err := Normalize(descriptor, nil, EpisodeParseInput{Kind: InputFileName, Text: "abc"}, EpisodeEvidenceDraft{
		Status: StatusValue, Shape: ShapeSingle, RawCapture: "1", Episode: "1", Span: &Span{Start: 2, End: 4},
	})
	if err == nil || !strings.Contains(err.Error(), "span") {
		t.Fatalf("expected span contract violation, got %v", err)
	}
	_, err = Normalize(descriptor, nil, EpisodeParseInput{Kind: InputFileName, Text: "Show"}, EpisodeEvidenceDraft{
		Status: StatusMissing, Shape: ShapeUnknown, Episode: "1",
	})
	if err == nil || !strings.Contains(err.Error(), "non-value status") {
		t.Fatalf("expected status/value contract violation, got %v", err)
	}
}

func TestRecognizeResolvesFileIdentityWithoutProviderEpisode(t *testing.T) {
	registry := NewDefaultRegistry()
	items := []RecognitionItem{{ClientRef: "video-1", Name: "Show - 07.mkv"}}
	result, err := Recognize(context.Background(), registry, items, RecognitionContext{
		WorkKind: "tv", Parser: ParserSelection{ID: ParserRegex},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || result[0].Identity.Episode == nil || *result[0].Identity.Episode != "7" {
		t.Fatalf("unexpected recognition result: %+v", result)
	}
}

type rejectingAdapter struct{}

func (rejectingAdapter) Descriptor() ParserDescriptor {
	return ParserDescriptor{ID: "rejecting", ImplementationVersion: "v1", OutputContractVersion: "evidence-v1"}
}
func (rejectingAdapter) ValidateConfig(json.RawMessage) error { return nil }
func (rejectingAdapter) ParseDraft(context.Context, EpisodeParseInput, json.RawMessage) (EpisodeEvidenceDraft, error) {
	return EpisodeEvidenceDraft{}, &selectedParserError{}
}

type selectedParserError struct{}

func (*selectedParserError) Error() string { return "selected parser failed" }

func intPtr(value int) *int { return &value }

func asContractError(err error, target **ParserContractError) bool {
	if err == nil {
		return false
	}
	value, ok := err.(*ParserContractError)
	if !ok {
		return false
	}
	*target = value
	return true
}
