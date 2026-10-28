// Package parser provides the side-effect-free media recognition capability
// shared by Release evaluation, Artifact inspection, FileManager, and Agent
// callers. It owns parser selection, registration, and result normalization; it
// never reads a path, contacts a provider, or writes an owner row.
package parser

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/episodeidentity"
)

const (
	InputReleaseTitle InputKind = "release_title"
	InputFileName     InputKind = "file_name"

	StatusValue       ParseStatus = "value"
	StatusMissing     ParseStatus = "missing"
	StatusInvalid     ParseStatus = "invalid"
	StatusUnsupported ParseStatus = "unsupported"
	StatusConflict    ParseStatus = "conflict"

	ShapeSingle     ParseShape = "single"
	ShapeRange      ParseShape = "range"
	ShapeList       ParseShape = "list"
	ShapeSeasonOnly ParseShape = "season_only"
	ShapeSpecial    ParseShape = "special"
	ShapeUnknown    ParseShape = "unknown"

	NormalizerVersion = "episode-normalizer-v1"
)

type InputKind string
type ParseStatus string
type ParseShape string

type Span struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

type EpisodeParseInput struct {
	Kind InputKind
	Text string
}

// ParserSelection identifies exactly one registered parser. There is no
// chain, fallback, merge, vote, or comparison pass after selection.
type ParserSelection struct {
	ID     string
	Config json.RawMessage
}

type ParserDescriptor struct {
	ID                    string
	ImplementationVersion string
	OutputContractVersion string
	DisplayName           string
	Capabilities          []string
	ConfigVersion         string
}

// EpisodeEvidenceDraft is the only parser-to-normalizer boundary. The
// forbidden fields are intentionally present so adapters decoding external
// model output can be rejected instead of silently allowing downstream policy
// to leak into parser evidence.
type EpisodeEvidenceDraft struct {
	Status     ParseStatus
	Shape      ParseShape
	RawCapture string
	Episode    string
	Season     *int
	Span       *Span
	Warnings   []string

	Offset       *int
	FinalEpisode string
	CoverageKeys []string
	TargetPath   string
	Naming       string
	Replacement  bool
}

type ParserAdapter interface {
	Descriptor() ParserDescriptor
	ValidateConfig(config json.RawMessage) error
	ParseDraft(ctx context.Context, input EpisodeParseInput, config json.RawMessage) (EpisodeEvidenceDraft, error)
}

type Registry struct {
	adapters map[string]ParserAdapter
}

var defaultRegistry = buildDefaultRegistry()

func NewRegistry() *Registry {
	return &Registry{adapters: make(map[string]ParserAdapter)}
}

func NewDefaultRegistry() *Registry {
	// Tests and callers that need an isolated registry may still request one;
	// production composition should use DefaultRegistry so every consumer
	// shares the same registered parser authority.
	return buildDefaultRegistry()
}

func buildDefaultRegistry() *Registry {
	r := NewRegistry()
	// The bundled parser is registered explicitly at the composition boundary;
	// its presence is not inferred from a requested parser id.
	if err := r.Register(RegexRuleAdapter{}); err != nil {
		panic(err)
	}
	if err := r.Register(ReltitleAdapter{}); err != nil {
		panic(err)
	}
	return r
}

// DefaultRegistry is the process registry wired by the composition root.
// Parser-policy and every other consumer must delegate to this instance
// instead of constructing a second parser authority.
func DefaultRegistry() *Registry { return defaultRegistry }

func (r *Registry) Register(adapter ParserAdapter) error {
	if r == nil || adapter == nil {
		return errors.New("media recognition parser registry: adapter is required")
	}
	descriptor := adapter.Descriptor()
	if strings.TrimSpace(descriptor.ID) == "" {
		return errors.New("media recognition parser registry: parser id is required")
	}
	if _, exists := r.adapters[descriptor.ID]; exists {
		return fmt.Errorf("media recognition parser %q is already registered", descriptor.ID)
	}
	r.adapters[descriptor.ID] = adapter
	return nil
}

func (r *Registry) Resolve(id string) (ParserAdapter, error) {
	if r == nil {
		return nil, errors.New("media recognition parser registry is nil")
	}
	adapter, ok := r.adapters[id]
	if !ok {
		return nil, fmt.Errorf("parser %q is not registered", id)
	}
	return adapter, nil
}

func (r *Registry) List() []ParserDescriptor {
	if r == nil {
		return nil
	}
	result := make([]ParserDescriptor, 0, len(r.adapters))
	for _, adapter := range r.adapters {
		result = append(result, adapter.Descriptor())
	}
	// The catalog order is the order every parser choice is offered in:
	// reltitle first, the rest by id.
	sort.Slice(result, func(i, j int) bool {
		if (result[i].ID == ParserReltitle) != (result[j].ID == ParserReltitle) {
			return result[i].ID == ParserReltitle
		}
		return result[i].ID < result[j].ID
	})
	return result
}

func (r *Registry) ValidateSelection(selection ParserSelection) (ParserAdapter, error) {
	adapter, err := r.Resolve(strings.TrimSpace(selection.ID))
	if err != nil {
		return nil, err
	}
	if err := adapter.ValidateConfig(selection.Config); err != nil {
		return nil, fmt.Errorf("parser %q config is invalid: %w", selection.ID, err)
	}
	return adapter, nil
}

type EpisodeParseResult struct {
	Status                      ParseStatus
	Shape                       ParseShape
	RawCapture                  string
	Episode                     *episodeidentity.EpisodeNumber
	SeasonEvidence              *int
	Span                        *Span
	Warnings                    []string
	ParserID                    string
	ParserImplementationVersion string
	ParserConfigFingerprint     string
	ParserConfigVersion         string
	OutputContractVersion       string
	NormalizerVersion           string
	InputDigest                 string
	ResultDigest                string
}

type ParserContractError struct{ Reason string }

func (e *ParserContractError) Error() string { return "PARSER_CONTRACT_VIOLATION: " + e.Reason }

func ParseEpisode(ctx context.Context, registry *Registry, selection ParserSelection, input EpisodeParseInput) (EpisodeParseResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	adapter, err := registry.ValidateSelection(selection)
	if err != nil {
		return EpisodeParseResult{}, err
	}
	draft, err := adapter.ParseDraft(ctx, input, selection.Config)
	if err != nil {
		return EpisodeParseResult{}, err
	}
	return Normalize(adapter.Descriptor(), selection.Config, input, draft)
}

func Normalize(descriptor ParserDescriptor, config json.RawMessage, input EpisodeParseInput, draft EpisodeEvidenceDraft) (EpisodeParseResult, error) {
	if strings.TrimSpace(descriptor.ID) == "" || strings.TrimSpace(descriptor.ImplementationVersion) == "" || strings.TrimSpace(descriptor.OutputContractVersion) == "" {
		return EpisodeParseResult{}, &ParserContractError{Reason: "descriptor version fields are required"}
	}
	if input.Kind != InputReleaseTitle && input.Kind != InputFileName {
		return EpisodeParseResult{}, &ParserContractError{Reason: "input kind is unsupported"}
	}
	if len(input.Text) > 8192 {
		return EpisodeParseResult{}, &ParserContractError{Reason: "input text exceeds 8192 bytes"}
	}
	if len(draft.RawCapture) > 1024 || len(draft.Warnings) > 16 {
		return EpisodeParseResult{}, &ParserContractError{Reason: "capture or warning bounds exceeded"}
	}
	if draft.Offset != nil || draft.FinalEpisode != "" || len(draft.CoverageKeys) != 0 || draft.TargetPath != "" || draft.Naming != "" || draft.Replacement {
		return EpisodeParseResult{}, &ParserContractError{Reason: "parser supplied downstream policy or final identity fields"}
	}
	if draft.Span != nil && (draft.Span.Start < 0 || draft.Span.End < draft.Span.Start || draft.Span.End > len(input.Text)) {
		return EpisodeParseResult{}, &ParserContractError{Reason: "evidence span is outside input bounds"}
	}
	if !validStatus(draft.Status) || !validShape(draft.Shape) {
		return EpisodeParseResult{}, &ParserContractError{Reason: "unknown status or shape"}
	}
	if draft.Status == StatusValue {
		if draft.Shape != ShapeSingle || strings.TrimSpace(draft.Episode) == "" {
			return EpisodeParseResult{}, &ParserContractError{Reason: "value status requires single shape and episode"}
		}
	} else if strings.TrimSpace(draft.Episode) != "" || draft.Season != nil {
		return EpisodeParseResult{}, &ParserContractError{Reason: "non-value status cannot carry scalar episode evidence"}
	}

	result := EpisodeParseResult{
		Status:                      draft.Status,
		Shape:                       draft.Shape,
		RawCapture:                  draft.RawCapture,
		SeasonEvidence:              draft.Season,
		Span:                        draft.Span,
		Warnings:                    append([]string(nil), draft.Warnings...),
		ParserID:                    descriptor.ID,
		ParserImplementationVersion: descriptor.ImplementationVersion,
		OutputContractVersion:       descriptor.OutputContractVersion,
		NormalizerVersion:           NormalizerVersion,
	}
	if result.Status == StatusValue {
		canonical, err := episodeidentity.CanonicalEpisode(draft.Episode)
		if err != nil {
			return EpisodeParseResult{}, &ParserContractError{Reason: "episode is not a canonical decimal"}
		}
		result.Episode = &canonical
	}
	result.ParserConfigVersion = descriptor.ConfigVersion
	result.ParserConfigFingerprint = digestBytes(config)
	result.InputDigest = digestJSON(struct {
		Kind           InputKind `json:"kind"`
		Text           string    `json:"text"`
		ParserID       string    `json:"parser_id"`
		ConfigHash     string    `json:"config_hash"`
		Implementation string    `json:"implementation_version"`
		ConfigVersion  string    `json:"config_version"`
	}{input.Kind, input.Text, descriptor.ID, result.ParserConfigFingerprint, descriptor.ImplementationVersion, descriptor.ConfigVersion})
	result.ResultDigest = digestJSON(result)
	return result, nil
}

func validStatus(value ParseStatus) bool {
	switch value {
	case StatusValue, StatusMissing, StatusInvalid, StatusUnsupported, StatusConflict:
		return true
	default:
		return false
	}
}

func validShape(value ParseShape) bool {
	switch value {
	case ShapeSingle, ShapeRange, ShapeList, ShapeSeasonOnly, ShapeSpecial, ShapeUnknown:
		return true
	default:
		return false
	}
}

func digestBytes(value []byte) string {
	return digestString(string(value))
}

func digestString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func digestJSON(value any) string {
	raw, _ := json.Marshal(value)
	return digestString(string(raw))
}

// RegexRuleAdapter is the bundled ordered-rule parser. It returns only raw
// episode evidence; offsets and coverage remain the resolver's responsibility.
type RegexRuleAdapter struct{}

func (RegexRuleAdapter) Descriptor() ParserDescriptor {
	return ParserDescriptor{
		ID: ParserRegex, ImplementationVersion: "regex-v1",
		OutputContractVersion: "episode-evidence-v1", DisplayName: "Regex rules",
		Capabilities: []string{"release_title", "file_name", "decimal"}, ConfigVersion: "regex-config-v1",
	}
}

func (RegexRuleAdapter) ValidateConfig(config json.RawMessage) error {
	if len(config) == 0 || strings.TrimSpace(string(config)) == "null" {
		return nil
	}
	var value struct {
		EpisodePatterns []string `json:"episode_patterns"`
	}
	if err := json.Unmarshal(config, &value); err != nil {
		return err
	}
	if len(value.EpisodePatterns) > 16 {
		return errors.New("episode_patterns is limited to 16 entries")
	}
	for _, pattern := range value.EpisodePatterns {
		if len(pattern) > 512 {
			return errors.New("episode pattern exceeds 512 bytes")
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("compile episode pattern: %w", err)
		}
	}
	return nil
}

func (a RegexRuleAdapter) ParseDraft(_ context.Context, input EpisodeParseInput, config json.RawMessage) (EpisodeEvidenceDraft, error) {
	title := strings.TrimSpace(input.Text)
	if title == "" {
		return EpisodeEvidenceDraft{Status: StatusMissing, Shape: ShapeUnknown}, nil
	}
	// Multi-episode shapes are detected before any scalar episode pattern, so a
	// range or list is never read as its first number.
	// The capture is the range or list text itself, so its members are the
	// evidence the resolver judges, not the rest of the title.
	if capture := regexRangePattern.FindString(title); capture != "" {
		return EpisodeEvidenceDraft{Status: StatusUnsupported, Shape: ShapeRange, RawCapture: trimCapture(capture)}, nil
	}
	if capture := regexListPattern.FindString(title); capture != "" {
		return EpisodeEvidenceDraft{Status: StatusUnsupported, Shape: ShapeList, RawCapture: trimCapture(capture)}, nil
	}
	patterns := []string(nil)
	if len(config) != 0 && strings.TrimSpace(string(config)) != "null" {
		var value struct {
			EpisodePatterns []string `json:"episode_patterns"`
		}
		if err := json.Unmarshal(config, &value); err != nil {
			return EpisodeEvidenceDraft{}, err
		}
		patterns = value.EpisodePatterns
	}
	var parsed acquisitiondomain.EpisodeResultExact
	var err error
	if len(patterns) == 0 {
		parsed, err = acquisitiondomain.ParseEpisodeExact(title, "", 0)
	} else {
		for _, pattern := range patterns {
			parsed, err = acquisitiondomain.ParseEpisodeExact(title, pattern, 0)
			if err == nil {
				break
			}
		}
	}
	if err != nil {
		return classifyRegexUnsupported(input, title), nil
	}
	return EpisodeEvidenceDraft{
		Status: StatusValue, Shape: ShapeSingle, RawCapture: parsed.RawCapture,
		Episode: string(parsed.EpisodeNumber), Span: captureSpan(title, parsed.RawCapture),
	}, nil
}

func classifyRegexUnsupported(input EpisodeParseInput, title string) EpisodeEvidenceDraft {
	switch {
	case regexSpecialPattern.MatchString(title):
		return EpisodeEvidenceDraft{Status: StatusUnsupported, Shape: ShapeSpecial, RawCapture: title}
	case regexSeasonPattern.MatchString(title):
		return EpisodeEvidenceDraft{Status: StatusUnsupported, Shape: ShapeSeasonOnly, RawCapture: title}
	default:
		return EpisodeEvidenceDraft{Status: StatusUnsupported, Shape: ShapeUnknown, RawCapture: title}
	}
}

var (
	regexRangePattern = regexp.MustCompile(`\b0*[0-9]{1,4}(?:\.[0-9]+)?\s*[~–—至]\s*0*[0-9]{1,4}(?:\.[0-9]+)?\b|(?:^|[\s._\[\]])0*[0-9]{1,4}(?:\.[0-9]+)?-0*[0-9]{1,4}(?:\.[0-9]+)?(?:[\s._\]\]]|$)|第\s*0*[0-9]{1,4}\s*[-~–—至到]\s*第?\s*0*[0-9]{1,4}\s*[话話集]|(?i:E0*[0-9]{1,4}\s*[-~–—]\s*E0*[0-9]{1,4}\b)`)
	// regexRangePattern: `09~16`, `[01-12]`, `第01-12话`, `E01-E04`.
	// regexListPattern: `S01E01E02` and adjacent bracketed episode
	// numbers `[01][02]`.
	regexListPattern    = regexp.MustCompile(`(?i)\bS[0-9]{1,2}E[0-9]{1,4}(?:E[0-9]{1,4})+\b|\[0*[0-9]{1,3}(?:\.[0-9]+)?\]\s*(?:\[0*[0-9]{1,3}(?:\.[0-9]+)?\]\s*)+`)
	regexSeasonPattern  = regexp.MustCompile(`(?i)\bS[0-9]{1,2}\b`)
	regexSpecialPattern = regexp.MustCompile(`(?i)\b(?:sp(?:ecial)?|ncop|nced|ova|ona|extra|pv|预告|特典)\b`)
)

// trimCapture drops the delimiters a range/list match carries at its edges.
func trimCapture(capture string) string {
	return strings.Trim(capture, " \t._")
}

func captureSpan(input, capture string) *Span {
	if capture == "" {
		return nil
	}
	start := strings.Index(input, capture)
	if start < 0 {
		return nil
	}
	return &Span{Start: start, End: start + len(capture)}
}

type RecognitionItem struct {
	ClientRef     string
	Name          string
	SizeBytes     *int64
	MediaKindHint *string
}

type RecognitionContext struct {
	WorkKind      string
	EpisodeOffset int
	Parser        ParserSelection
}

type RecognizedMedia struct {
	ClientRef       string
	MediaKind       string
	Disposition     string
	Episode         EpisodeParseResult
	Identity        episodeidentity.EpisodeIdentity
	Warnings        []string
	ParserVersion   string
	ResolverVersion string
	InputDigest     string
}

func Recognize(ctx context.Context, registry *Registry, items []RecognitionItem, input RecognitionContext) ([]RecognizedMedia, error) {
	if len(items) == 0 || len(items) > 256 {
		return nil, errors.New("recognition items must contain 1..256 entries")
	}
	if input.WorkKind != "tv" && input.WorkKind != "movie" {
		return nil, errors.New("recognition work kind must be tv or movie")
	}
	if _, err := registry.ValidateSelection(input.Parser); err != nil {
		return nil, err
	}
	result := make([]RecognizedMedia, 0, len(items))
	resolver := episodeidentity.NewResolver()
	for _, item := range items {
		parsed, err := ParseEpisode(ctx, registry, input.Parser, EpisodeParseInput{Kind: InputFileName, Text: item.Name})
		if err != nil {
			return nil, err
		}
		identity := resolver.Resolve(episodeidentity.EpisodeIdentityInput{
			MediaKind: input.WorkKind, FileRef: item.ClientRef,
			FileEvidence: Evidence(parsed, InputFileName),
			Offset:       input.EpisodeOffset,
		})
		mediaKind := "video"
		if item.MediaKindHint != nil && strings.TrimSpace(*item.MediaKindHint) != "" {
			mediaKind = strings.TrimSpace(*item.MediaKindHint)
		}
		disposition := "classified"
		if identity.State != episodeidentity.StateResolved || mediaKind != "video" {
			disposition = "unclassified"
		}
		result = append(result, RecognizedMedia{
			ClientRef: item.ClientRef, MediaKind: mediaKind, Disposition: disposition,
			Episode: parsed, Identity: identity, Warnings: parsed.Warnings,
			ParserVersion: parsed.ParserImplementationVersion, ResolverVersion: identity.ResolverVersion,
			InputDigest: parsed.InputDigest,
		})
	}
	return result, nil
}

// Evidence converts normalized parser output to the legacy resolver evidence
// envelope. It is a projection only; it does not parse or apply offsets.
func Evidence(result EpisodeParseResult, kind InputKind) episodeidentity.EpisodeEvidence {
	source := episodeidentity.EpisodeSourceFileParser
	if kind == InputReleaseTitle {
		source = episodeidentity.EpisodeSourceReleaseParser
	}
	evidence := episodeidentity.EpisodeEvidence{
		Source: source, InputKind: string(kind), Status: string(result.Status), Shape: string(result.Shape),
		RawCapture: result.RawCapture,
	}
	if result.Episode != nil {
		episode := *result.Episode
		evidence.Episode = &episode
	}
	return evidence
}
