package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"bansoko/internal/catalog/domain"
	hardeningtransport "bansoko/internal/hardening/transport"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/runtime/buildinfo"
	"github.com/PuerkitoBio/goquery"
)

const mikanSeasonContractVersion = "mikan-season-html-v1"

type MikanHTTPReader struct {
	Endpoint string
	Client   *http.Client
	Recorder observabilityport.Recorder
}

func (r MikanHTTPReader) ReadSeason(ctx context.Context, season string, hints domain.ReadHints) (domain.ReadResult, error) {
	year, seasonName, normalizedSeason, err := normalizeMikanSeason(season)
	if err != nil {
		return domain.ReadResult{}, err
	}
	base, err := url.Parse(strings.TrimRight(r.Endpoint, "/"))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Hostname() == "" || base.User != nil || base.Fragment != "" {
		return domain.ReadResult{}, errors.New("Mikan catalog endpoint must be an absolute HTTP URL without credentials")
	}
	assetBase := *base
	assetBase.Path = strings.TrimRight(assetBase.Path, "/") + "/"
	base.Path = strings.TrimRight(base.Path, "/") + "/Home/BangumiCoverFlowByDayOfWeek"
	query := base.Query()
	query.Set("year", year)
	query.Set("seasonStr", seasonName)
	base.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return domain.ReadResult{}, fmt.Errorf("create Mikan catalog request: %w", err)
	}
	request.Header.Set("Accept", "text/html,application/xhtml+xml")
	request.Header.Set("User-Agent", buildinfo.UserAgent("catalog"))
	if hints.ETag != "" {
		request.Header.Set("If-None-Match", hints.ETag)
	}
	if hints.LastModified != "" {
		request.Header.Set("If-Modified-Since", hints.LastModified)
	}
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second, Transport: hardeningtransport.DirectTransport()}
	}
	response, err := client.Do(request)
	if err != nil {
		return domain.ReadResult{}, &domain.ReadError{Code: domain.ReadFailureUnreachable, Err: fmt.Errorf("fetch Mikan catalog: %w", err)}
	}
	defer response.Body.Close()
	etag := response.Header.Get("ETag")
	lastModified := response.Header.Get("Last-Modified")
	if response.StatusCode == http.StatusNotModified {
		return domain.ReadResult{SourceVersion: normalizedSeason, Cursor: hints.Cursor, ETag: etag, LastModified: lastModified, ContractVersion: mikanSeasonContractVersion, NotModified: true}, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return domain.ReadResult{}, &domain.ReadError{Code: domain.ReadFailureHTTPStatus, Err: fmt.Errorf("Mikan catalog returned HTTP %d", response.StatusCode)}
	}
	document, err := goquery.NewDocumentFromReader(response.Body)
	if err != nil {
		return domain.ReadResult{}, fmt.Errorf("parse Mikan catalog HTML: %w", err)
	}
	observedSchemaVersion, hasSchemaMarker := mikanCatalogSchemaVersionFromResponse(document, response.Header)
	schemaMismatch := hasSchemaMarker && observedSchemaVersion != mikanSeasonContractVersion
	scopeID := "mikan-catalog-" + normalizedSeason
	if schemaMismatch {
		recordMikanCatalogParserWarning(ctx, r.Recorder, scopeID, season, "provider_parser_schema_mismatch", mikanSeasonContractVersion, observedSchemaVersion)
	}
	containers := document.Find("[data-dayofweek]")
	if containers.Length() == 0 {
		if mikanSeasonNotPopulated(document) {
			// Measured against the real site: a valid season that Mikan
			// has not populated (just started, future, or too old) answers HTTP 200
			// with only the shared page preamble and no weekday containers. That is
			// "no content yet", not a parser break, so it is a valid empty result
			// and raises no diagnostic.
			closeMikanCatalogParserWarning(ctx, r.Recorder, scopeID, "provider_parser_empty")
			if !schemaMismatch {
				closeMikanCatalogParserWarning(ctx, r.Recorder, scopeID, "provider_parser_schema_mismatch")
				restoreMikanCatalogRoutine(ctx, r.Recorder, scopeID)
			}
			return domain.ReadResult{
				SourceVersion: normalizedSeason, Cursor: normalizedSeason,
				ETag: etag, LastModified: lastModified, ContractVersion: mikanSeasonContractVersion,
			}, nil
		}
		if !schemaMismatch {
			recordMikanCatalogParserWarning(ctx, r.Recorder, scopeID, season, "provider_parser_schema_mismatch", mikanSeasonContractVersion, "missing:[data-dayofweek]")
		}
		return domain.ReadResult{}, &domain.ReadError{Code: domain.ReadFailureSchemaMismatch, Err: errors.New("Mikan catalog HTML has no weekday containers")}
	}
	entries := make([]domain.Entry, 0)
	seen := make(map[string]struct{})
	var parseErr error
	containers.EachWithBreak(func(_ int, container *goquery.Selection) bool {
		weekday := strings.TrimSpace(selectionAttr(container, "data-dayofweek"))
		container.Find("li").EachWithBreak(func(_ int, item *goquery.Selection) bool {
			span := item.Find("span[data-bangumiid]").First()
			if span.Length() == 0 {
				if strings.TrimSpace(item.Text()) != "" || item.Find("*").Length() > 0 {
					parseErr = errors.New("Mikan catalog item has no bangumi ID")
					return false
				}
				return true
			}
			externalID, err := normalizedCatalogID(selectionAttr(span, "data-bangumiid"))
			if err != nil {
				parseErr = err
				return false
			}
			if _, exists := seen[externalID]; exists {
				parseErr = fmt.Errorf("Mikan catalog contains duplicate bangumi ID %s", externalID)
				return false
			}
			link := item.Find("a.an-text").First()
			title := strings.TrimSpace(selectionAttr(link, "title"))
			if title == "" {
				title = strings.TrimSpace(link.Text())
			}
			if title == "" {
				title = mikanGreyoutTitle(item)
			}
			if title == "" {
				parseErr = fmt.Errorf("Mikan catalog entry %s has no title", externalID)
				return false
			}
			seen[externalID] = struct{}{}
			updateText := strings.TrimSpace(item.Find("div.date-text").First().Text())
			updateText = strings.TrimSpace(strings.TrimSuffix(updateText, "更新"))
			// The site greys out entries that have no works yet; record that
			// page fact so the catalog can mask them the same way. The greyout
			// placeholder line is not an update string.
			greyout := span.HasClass("greyout")
			if updateText == mikanGreyoutPlaceholder {
				updateText = ""
			}
			entries = append(entries, domain.Entry{
				ProviderEntryRef: externalID, Title: title, State: domain.EntryPresent,
				SourceHint: map[string]any{
					"provider": "mikan", "bangumi_id": externalID,
					"cover":   resolveMikanAssetURL(assetBase, selectionAttr(span, "data-src")),
					"weekday": weekday, "update_text": updateText,
					"season": normalizedSeason, "greyout": greyout,
				},
			})
			return true
		})
		return parseErr == nil
	})
	if parseErr != nil {
		return domain.ReadResult{}, &domain.ReadError{Code: domain.ReadFailureSchemaMismatch, Err: parseErr}
	}
	if len(entries) == 0 {
		recordMikanCatalogParserWarning(ctx, r.Recorder, scopeID, season, "provider_parser_empty", mikanSeasonContractVersion, observedSchemaVersion)
		if !schemaMismatch {
			closeMikanCatalogParserWarning(ctx, r.Recorder, scopeID, "provider_parser_schema_mismatch")
		}
	} else {
		closeMikanCatalogParserWarning(ctx, r.Recorder, scopeID, "provider_parser_empty")
		if !schemaMismatch {
			closeMikanCatalogParserWarning(ctx, r.Recorder, scopeID, "provider_parser_schema_mismatch")
			restoreMikanCatalogRoutine(ctx, r.Recorder, scopeID)
		}
	}
	return domain.ReadResult{
		Entries: entries, SourceVersion: normalizedSeason, Cursor: normalizedSeason,
		ETag: etag, LastModified: lastModified, ContractVersion: mikanSeasonContractVersion,
	}, nil
}

// mikanSeasonNotPopulated reports whether a page without weekday containers is
// still recognisably Mikan's season template. The empty-season body is the
// shared SVG preamble (clip-triangle-N, row-bg-gradient) only; an error page,
// a captive portal or a redesigned page carries none of those ids.
func mikanSeasonNotPopulated(document *goquery.Document) bool {
	return document.Find(`[id="row-bg-gradient"], [id^="clip-triangle-"]`).Length() > 0
}

func mikanCatalogSchemaVersionFromResponse(document *goquery.Document, headers http.Header) (string, bool) {
	for _, header := range []string{"X-Mikan-Schema-Version", "X-Schema-Version", "X-Parser-Schema-Version"} {
		if version := strings.TrimSpace(headers.Get(header)); version != "" {
			return version, true
		}
	}
	for _, selector := range []string{`meta[name="mikan-schema-version"]`, `[data-mikan-schema-version]`, `[data-schema-version]`} {
		selection := document.Find(selector).First()
		if version, ok := selection.Attr("content"); ok && strings.TrimSpace(version) != "" {
			return strings.TrimSpace(version), true
		}
		if version, ok := selection.Attr("data-mikan-schema-version"); ok && strings.TrimSpace(version) != "" {
			return strings.TrimSpace(version), true
		}
		if version, ok := selection.Attr("data-schema-version"); ok && strings.TrimSpace(version) != "" {
			return strings.TrimSpace(version), true
		}
	}
	return "", false
}

func recordMikanCatalogParserWarning(ctx context.Context, recorder observabilityport.Recorder, scopeID, season, errorCode, expectedSchemaVersion, observedSchemaVersion string) {
	if recorder == nil {
		return
	}
	details, _ := json.Marshal(map[string]string{
		"provider":                "mikan",
		"source":                  "season_catalog",
		"season":                  season,
		"response_path":           "Home/BangumiCoverFlowByDayOfWeek",
		"expected_schema_version": expectedSchemaVersion,
		"observed_schema_version": observedSchemaVersion,
	})
	if err := recorder.RecordDiagnosticFailure(ctx, observabilityport.DiagnosticFailureRequest{
		Identity: observabilityport.ProviderRefreshFailure(observabilityport.DiagnosticErrorCode(errorCode)), Severity: observabilityport.SeverityWarning,
		ScopeID: scopeID, LastError: "Mikan catalog parser warning", Details: details,
	}); err != nil {
		observabilityport.RecordDiagnosticError(nil, "provider", "refresh", scopeID, errorCode, "diagnostic_write_failed")
	}
	if err := recorder.UpsertRoutine(ctx, observabilityport.RoutineRequest{
		Identity: observabilityport.ProviderRefreshRoutine(observabilityport.RoutineOutcomeWarning, observabilityport.RoutineResultProviderParserWarning),
		ScopeID:  scopeID, Summary: "Mikan catalog parser warning", Details: details,
	}); err != nil {
		observabilityport.RecordRoutineError(nil, "provider_refresh", "provider", scopeID, "routine_write_failed")
	}
}

func closeMikanCatalogParserWarning(ctx context.Context, recorder observabilityport.Recorder, scopeID, errorCode string) {
	if recorder == nil {
		return
	}
	if err := recorder.RecordDiagnosticSuccess(ctx, observabilityport.DiagnosticSuccessRequest{
		Identity: observabilityport.ProviderRefreshFailure(observabilityport.DiagnosticErrorCode(errorCode)), ScopeID: scopeID,
	}); err != nil {
		observabilityport.RecordDiagnosticError(nil, "provider", "refresh", scopeID, errorCode, "diagnostic_close_failed")
	}
}

func restoreMikanCatalogRoutine(ctx context.Context, recorder observabilityport.Recorder, scopeID string) {
	if recorder == nil {
		return
	}
	if err := recorder.UpsertRoutine(ctx, observabilityport.RoutineRequest{
		Identity: observabilityport.ProviderRefreshRoutine(observabilityport.RoutineOutcomeSuccess, observabilityport.RoutineResultProviderRefreshCompleted),
		ScopeID:  scopeID, Summary: "Mikan catalog parser recovered",
	}); err != nil {
		observabilityport.RecordRoutineError(nil, "provider_refresh", "provider", scopeID, "routine_write_failed")
	}
}

func normalizeMikanSeason(value string) (string, string, string, error) {
	value = strings.TrimSpace(strings.ToLower(value))
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == '-' || r == '_' || r == ' ' })
	if len(parts) != 2 {
		return "", "", "", errors.New("Mikan season must use YYYY-season or season-YYYY")
	}
	year, season := parts[0], parts[1]
	if _, err := strconv.Atoi(year); err != nil {
		year, season = parts[1], parts[0]
	}
	yearValue, err := strconv.Atoi(year)
	if err != nil || yearValue < 2000 || yearValue > 2100 {
		return "", "", "", errors.New("Mikan season year is invalid")
	}
	seasonNames := map[string]string{"winter": "冬", "spring": "春", "summer": "夏", "fall": "秋", "autumn": "秋"}
	localized, ok := seasonNames[season]
	if !ok {
		return "", "", "", errors.New("Mikan season name is invalid")
	}
	if season == "autumn" {
		season = "fall"
	}
	return strconv.Itoa(yearValue), localized, fmt.Sprintf("%d-%s", yearValue, season), nil
}

func normalizedCatalogID(value string) (string, error) {
	parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
	if err != nil || parsed == 0 {
		return "", errors.New("Mikan bangumi ID must be a positive integer")
	}
	return strconv.FormatUint(parsed, 10), nil
}

func resolveMikanAssetURL(base url.URL, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	asset, err := url.Parse(value)
	if err != nil || asset.IsAbs() && (asset.Scheme != "http" && asset.Scheme != "https") {
		return ""
	}
	resolved := base.ResolveReference(asset)
	if resolved.Scheme != "http" && resolved.Scheme != "https" || resolved.Hostname() == "" || resolved.User != nil || resolved.Fragment != "" {
		return ""
	}
	return resolved.String()
}

func selectionAttr(selection *goquery.Selection, name string) string {
	value, _ := selection.Attr(name)
	return value
}

// mikanGreyoutPlaceholder is the text Mikan renders in place of the update
// date for an entry without works.
const mikanGreyoutPlaceholder = "此番组下暂无作品"

// Some Mikan pages keep withdrawn/no-release entries in the season grid as a
// greyed-out span. They have no .an-text link; the title is carried by the
// title attribute of the second date-text node. This is still a catalog fact,
// so retain it instead of treating the whole snapshot as a parser outage.
func mikanGreyoutTitle(item *goquery.Selection) string {
	var title string
	item.Find("div.an-info-group .date-text").EachWithBreak(func(_ int, node *goquery.Selection) bool {
		candidate := strings.TrimSpace(selectionAttr(node, "title"))
		if candidate == "" {
			candidate = strings.TrimSpace(node.Text())
		}
		if candidate == "" || candidate == mikanGreyoutPlaceholder || strings.HasSuffix(candidate, "更新") {
			return true
		}
		title = candidate
		return false
	})
	return title
}
