package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bansoko/internal/parser"
	"bansoko/internal/transport/httpapi/generated"
)

func mediaRecognitionInput(parser string, count int) generated.MediaRecognitionPreviewInput {
	items := make([]generated.MediaRecognitionItemInput, 0, count)
	for index := 0; index < count; index++ {
		items = append(items, generated.MediaRecognitionItemInput{
			ClientRef: "file-" + string(rune('a'+index%26)),
			Name:      "Show - 01.mkv",
		})
	}
	return generated.MediaRecognitionPreviewInput{
		Items: items,
		Context: generated.MediaRecognitionContext{
			WorkKind: generated.MediaRecognitionContextWorkKindTv,
		},
		Parser: generated.ParserSelection{Id: parser},
	}
}

func TestMediaRecognitionPreviewEnforcesBatchBoundsAndParserSelection(t *testing.T) {
	service := NewMediaRecognitionService(parser.NewDefaultRegistry())
	if _, err := service.Preview(context.Background(), mediaRecognitionInput(parser.ParserRegex, 0)); err == nil {
		t.Fatal("empty batch must be rejected")
	}
	if _, err := service.Preview(context.Background(), mediaRecognitionInput(parser.ParserRegex, 257)); err == nil {
		t.Fatal("batch above 256 must be rejected")
	}
	if _, err := service.Preview(context.Background(), mediaRecognitionInput("missing", 1)); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("unknown parser must fail without fallback: %v", err)
	}
}

func TestMediaRecognitionPreviewUsesLogicalTextOnlyAndReturnsContractErrors(t *testing.T) {
	registry := parser.NewRegistry()
	if err := registry.Register(httpRejectingAdapter{}); err != nil {
		t.Fatal(err)
	}
	service := NewMediaRecognitionService(registry)
	input := mediaRecognitionInput("reject", 1)
	input.Items[0].Name = "/this/path/is/logical-only.mkv"
	if _, err := service.Preview(context.Background(), input); err == nil || !strings.Contains(err.Error(), "selected parser failed") {
		t.Fatalf("selected parser failure must be returned directly: %v", err)
	}

	contractRegistry := parser.NewRegistry()
	if err := contractRegistry.Register(httpPolicyLeakAdapter{}); err != nil {
		t.Fatal(err)
	}
	contractService := NewMediaRecognitionService(contractRegistry)
	contractInput := mediaRecognitionInput("leak", 1)
	if _, err := contractService.Preview(context.Background(), contractInput); err == nil || !strings.Contains(err.Error(), "PARSER_CONTRACT_VIOLATION") {
		t.Fatalf("parser policy leak must be a typed contract error: %v", err)
	}
}

func TestMediaRecognitionHTTPPreviewAndCatalog(t *testing.T) {
	server := &Server{deps: Dependencies{
		MediaRecognition: NewMediaRecognitionService(parser.DefaultRegistry()),
	}}

	body, err := json.Marshal(mediaRecognitionInput(parser.ParserRegex, 1))
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/media-recognition/previews", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	server.PreviewMediaRecognition(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var result generated.MediaRecognitionPreviewResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].ClientRef == "" {
		t.Fatalf("unexpected preview result: %+v", result)
	}

	recorder = httptest.NewRecorder()
	server.ListParserCatalog(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/media-recognition/parser-catalog", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), parser.ParserRegex) {
		t.Fatalf("catalog status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

type httpRejectingAdapter struct{}

func (httpRejectingAdapter) Descriptor() parser.ParserDescriptor {
	return parser.ParserDescriptor{ID: "reject", ImplementationVersion: "v1", OutputContractVersion: "episode-evidence-v1"}
}
func (httpRejectingAdapter) ValidateConfig(json.RawMessage) error { return nil }
func (httpRejectingAdapter) ParseDraft(context.Context, parser.EpisodeParseInput, json.RawMessage) (parser.EpisodeEvidenceDraft, error) {
	return parser.EpisodeEvidenceDraft{}, errors.New("selected parser failed")
}

type httpPolicyLeakAdapter struct{}

func (httpPolicyLeakAdapter) Descriptor() parser.ParserDescriptor {
	return parser.ParserDescriptor{ID: "leak", ImplementationVersion: "v1", OutputContractVersion: "episode-evidence-v1"}
}
func (httpPolicyLeakAdapter) ValidateConfig(json.RawMessage) error { return nil }
func (httpPolicyLeakAdapter) ParseDraft(context.Context, parser.EpisodeParseInput, json.RawMessage) (parser.EpisodeEvidenceDraft, error) {
	return parser.EpisodeEvidenceDraft{Status: parser.StatusValue, Shape: parser.ShapeSingle, Episode: "1", CoverageKeys: []string{"tv:e1"}}, nil
}
