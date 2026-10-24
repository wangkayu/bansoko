package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"bansoko/internal/parser"
	"bansoko/internal/transport/httpapi/generated"
)

// PreviewMediaRecognition delegates the complete batch to the one registered
// parser selection. The HTTP boundary accepts logical text only; it never
// turns a caller-supplied path into a filesystem read.
func (s *Server) PreviewMediaRecognition(w http.ResponseWriter, r *http.Request) {
	var input generated.MediaRecognitionPreviewInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	result, err := s.deps.MediaRecognition.Preview(r.Context(), input)
	if err != nil {
		writeError(w, r, unprocessable("MEDIA_RECOGNITION_INVALID", err.Error(), nil))
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, result)
}

// ListParserCatalog exposes only code-owned registered parser descriptors.
func (s *Server) ListParserCatalog(w http.ResponseWriter, r *http.Request) {
	result, err := s.deps.MediaRecognition.ListParsers(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, result)
}

type mediaRecognitionService struct{ registry *parser.Registry }

// NewMediaRecognitionService builds the transport adapter over the shared
// process parser registry. The registry remains the only parser authority.
func NewMediaRecognitionService(registry *parser.Registry) MediaRecognitionService {
	if registry == nil {
		registry = parser.DefaultRegistry()
	}
	return &mediaRecognitionService{registry: registry}
}

func (s *mediaRecognitionService) Preview(ctx context.Context, input generated.MediaRecognitionPreviewInput) (generated.MediaRecognitionPreviewResult, error) {
	if s == nil || s.registry == nil {
		return generated.MediaRecognitionPreviewResult{}, errors.New("media recognition registry is not configured")
	}
	if len(input.Items) == 0 || len(input.Items) > 256 {
		return generated.MediaRecognitionPreviewResult{}, errors.New("items must contain 1..256 entries")
	}
	config, err := json.Marshal(input.Parser.Config)
	if input.Parser.Config == nil {
		config = nil
	}
	if err != nil {
		return generated.MediaRecognitionPreviewResult{}, err
	}
	items := make([]parser.RecognitionItem, 0, len(input.Items))
	for _, item := range input.Items {
		if strings.TrimSpace(item.ClientRef) == "" || strings.TrimSpace(item.Name) == "" {
			return generated.MediaRecognitionPreviewResult{}, errors.New("client_ref and name are required")
		}
		if len(item.ClientRef) > 256 || len(item.Name) > 8192 {
			return generated.MediaRecognitionPreviewResult{}, errors.New("client_ref/name exceeds the declared input bound")
		}
		if item.SizeBytes != nil && *item.SizeBytes < 0 {
			return generated.MediaRecognitionPreviewResult{}, errors.New("size_bytes must be non-negative")
		}
		items = append(items, parser.RecognitionItem{
			ClientRef: item.ClientRef, Name: item.Name, SizeBytes: item.SizeBytes,
			MediaKindHint: item.MediaKindHint,
		})
	}
	season, seasonBound, offset := 0, false, 0
	if input.Context.Season != nil {
		season = *input.Context.Season
	}
	if input.Context.SeasonBound != nil {
		seasonBound = *input.Context.SeasonBound
	}
	if input.Context.EpisodeOffset != nil {
		offset = *input.Context.EpisodeOffset
	}
	recognized, err := parser.Recognize(ctx, s.registry, items, parser.RecognitionContext{
		WorkKind: string(input.Context.WorkKind), Season: season, SeasonBound: seasonBound,
		EpisodeOffset: offset, Parser: parser.ParserSelection{ID: input.Parser.Id, Config: config},
	})
	if err != nil {
		return generated.MediaRecognitionPreviewResult{}, err
	}
	result := generated.MediaRecognitionPreviewResult{Items: make([]generated.MediaRecognitionItemResult, 0, len(recognized))}
	for _, item := range recognized {
		mapped := generated.MediaRecognitionItemResult{
			ClientRef: item.ClientRef, MediaKind: item.MediaKind,
			Disposition:  generated.MediaRecognitionItemResultDisposition(item.Disposition),
			ParserStatus: string(item.Episode.Status), ParserShape: string(item.Episode.Shape),
			IdentityState: item.Identity.State, ParserVersion: item.ParserVersion,
			ResolverVersion: item.ResolverVersion, InputDigest: item.InputDigest,
		}
		if item.Episode.RawCapture != "" {
			value := item.Episode.RawCapture
			mapped.RawCapture = &value
		}
		if item.Episode.Episode != nil {
			value := string(*item.Episode.Episode)
			mapped.Episode = &value
		}
		if item.Identity.Episode != nil {
			value := string(*item.Identity.Episode)
			mapped.FinalEpisode = &value
		}
		if item.Identity.CoverageKey != nil {
			value := *item.Identity.CoverageKey
			mapped.CoverageKey = &value
		}
		if len(item.Warnings) > 0 {
			warnings := append([]string(nil), item.Warnings...)
			mapped.Warnings = &warnings
		}
		result.Items = append(result.Items, mapped)
		if result.ParserVersion == "" {
			result.ParserVersion = item.ParserVersion
		}
		if result.ResolverVersion == "" {
			result.ResolverVersion = item.ResolverVersion
		}
	}
	return result, nil
}

func (s *mediaRecognitionService) ListParsers(_ context.Context) ([]generated.ParserCatalogEntry, error) {
	if s == nil || s.registry == nil {
		return nil, errors.New("media recognition registry is not configured")
	}
	descriptors := s.registry.List()
	result := make([]generated.ParserCatalogEntry, 0, len(descriptors))
	for _, descriptor := range descriptors {
		result = append(result, generated.ParserCatalogEntry{
			Id: descriptor.ID, ImplementationVersion: descriptor.ImplementationVersion,
			OutputContractVersion: descriptor.OutputContractVersion, DisplayName: descriptor.DisplayName,
			Capabilities: append([]string(nil), descriptor.Capabilities...), ConfigVersion: descriptor.ConfigVersion,
		})
	}
	return result, nil
}
