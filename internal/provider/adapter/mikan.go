package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	observabilityport "bansoko/internal/observability/port"
	providerdomain "bansoko/internal/provider/domain"
	"bansoko/internal/runtime/buildinfo"
	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"
)

const mikanGroupSchemaVersion = "mikan-detail-groups-v1"

type MikanGroupReader struct {
	Endpoint string
	Client   *http.Client
	Now      func() time.Time
	Recorder observabilityport.Recorder
}

func (r MikanGroupReader) ReadGroups(ctx context.Context, catalogEntryRef string) (providerdomain.GroupReadResult, error) {
	mikanID, err := strconv.ParseUint(strings.TrimSpace(catalogEntryRef), 10, 64)
	if err != nil || mikanID == 0 || strconv.FormatUint(mikanID, 10) != catalogEntryRef {
		return providerdomain.GroupReadResult{}, errors.New("Mikan bangumi ID must be a normalized positive integer")
	}
	endpoint, err := providerEndpoint(r.Endpoint, "/Home/Bangumi/"+catalogEntryRef)
	if err != nil {
		return providerdomain.GroupReadResult{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return providerdomain.GroupReadResult{}, fmt.Errorf("create Mikan group request: %w", err)
	}
	request.Header.Set("Accept", "text/html,application/xhtml+xml")
	request.Header.Set("User-Agent", buildinfo.UserAgent("provider"))
	response, err := httpClient(r.Client).Do(request)
	if err != nil {
		return providerdomain.GroupReadResult{}, fmt.Errorf("fetch Mikan groups: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return providerdomain.GroupReadResult{}, fmt.Errorf("Mikan groups returned HTTP %d", response.StatusCode)
	}
	payload, err := readBoundedResponse(response)
	if err != nil {
		return providerdomain.GroupReadResult{}, fmt.Errorf("read Mikan groups: %w", err)
	}
	document, err := goquery.NewDocumentFromReader(bytes.NewReader(payload))
	if err != nil {
		return providerdomain.GroupReadResult{}, fmt.Errorf("parse Mikan detail HTML: %w", err)
	}
	observedAt := time.Now().UTC()
	if r.Now != nil {
		observedAt = r.Now().UTC()
	}
	result := providerdomain.GroupReadResult{Provider: providerdomain.ProviderMikan, CatalogEntryRef: catalogEntryRef, ObservedAt: observedAt, SourceVersion: response.Header.Get("ETag"), SchemaVersion: mikanGroupSchemaVersion, Groups: make([]providerdomain.GroupSnapshotEntry, 0)}
	observedSchemaVersion, hasSchemaMarker := mikanGroupSchemaVersionFromResponse(document, response.Header)
	schemaMismatch := hasSchemaMarker && observedSchemaVersion != mikanGroupSchemaVersion
	if schemaMismatch {
		recordMikanGroupParserWarning(ctx, r.Recorder, catalogEntryRef, "provider_parser_schema_mismatch", mikanGroupSchemaVersion, observedSchemaVersion)
	}
	result.Complete = !schemaMismatch && mikanGroupTitleAnchorPresent(document)
	result.CompletenessEvidence = "mikan-detail-group-blocks-v1"
	groupBlocks := document.Find("div.subgroup-text")
	if groupBlocks.Length() == 0 {
		// A normal Mikan bangumi detail page always renders its title container
		// even when no subgroup has published yet (new title before broadcast,
		// cold entries). Only flag an empty result when the title anchor is
		// missing, which signals a page layout change rather than a legitimate
		// empty snapshot. A schema mismatch stays the primary signal and still
		// suppresses the empty-result warning.
		if !schemaMismatch && !mikanGroupTitleAnchorPresent(document) {
			recordMikanGroupParserWarning(ctx, r.Recorder, catalogEntryRef, "provider_parser_empty", mikanGroupSchemaVersion, observedSchemaVersion)
		}
		return result, nil
	}
	seen := make(map[string]struct{})
	var parseErr error
	groupBlocks.EachWithBreak(func(_ int, group *goquery.Selection) bool {
		rssHref, exists := group.Find("a[href*='/RSS/Bangumi']").First().Attr("href")
		if !exists {
			result.Complete = false
			return true
		}
		rssURL, err := url.Parse(strings.TrimSpace(rssHref))
		if err != nil {
			parseErr = fmt.Errorf("parse Mikan group RSS link: %w", err)
			return false
		}
		if bangumi := rssURL.Query().Get("bangumiId"); bangumi != catalogEntryRef {
			parseErr = errors.New("Mikan group RSS link belongs to a different bangumi")
			return false
		}
		ref, err := providerdomain.NewGroupRef(providerdomain.ProviderMikan, rssURL.Query().Get("subgroupid"))
		if err != nil {
			parseErr = fmt.Errorf("invalid Mikan group RSS link: %w", err)
			return false
		}
		name := strings.TrimSpace(group.Find("a[href*='/Home/PublishGroup/']").First().Text())
		if name == "" {
			group.Contents().EachWithBreak(func(_ int, node *goquery.Selection) bool {
				if raw := node.Get(0); raw != nil && raw.Type == html.TextNode {
					name = strings.TrimSpace(raw.Data)
				}
				return name == ""
			})
		}
		if name == "" {
			parseErr = fmt.Errorf("Mikan group %q has no display name", ref.ExternalID)
			return false
		}
		if _, exists := seen[ref.Key()]; exists {
			parseErr = fmt.Errorf("Mikan groups contain duplicate subgroup ID %q", ref.ExternalID)
			return false
		}
		seen[ref.Key()] = struct{}{}
		active := true
		result.Groups = append(result.Groups, providerdomain.GroupSnapshotEntry{
			Ref: ref, DisplayName: name, ObservedAt: observedAt,
			Active:      &active,
			FeedLocator: providerdomain.FeedLocator{Provider: providerdomain.ProviderMikan, CatalogEntryRef: catalogEntryRef, Group: ref},
		})
		return true
	})
	if parseErr != nil {
		return providerdomain.GroupReadResult{}, parseErr
	}
	if len(result.Groups) == 0 {
		recordMikanGroupParserWarning(ctx, r.Recorder, catalogEntryRef, "provider_parser_empty", mikanGroupSchemaVersion, observedSchemaVersion)
		return result, nil
	}
	closeMikanGroupParserWarning(ctx, r.Recorder, catalogEntryRef, "provider_parser_empty")
	if !schemaMismatch {
		closeMikanGroupParserWarning(ctx, r.Recorder, catalogEntryRef, "provider_parser_schema_mismatch")
		restoreMikanGroupRoutine(ctx, r.Recorder, catalogEntryRef)
	}
	return result, nil
}

// mikanGroupPageTitlePrefix is the title marker every live Mikan group page
// carries: `<title>Mikan Project - <display name></title>`. It is the same
// discriminator the subgroup dataset used to tell a live id from a retired one.
const mikanGroupPageTitlePrefix = "Mikan Project - "

// maxMikanGroupTitleLength bounds the display name taken from the page title,
// so a broken or hostile page cannot inflate one candidate unboundedly.
const maxMikanGroupTitleLength = 256

// ReadGroupByID confirms one Mikan subgroup id against its public group page
// and returns the display name that page publishes.
//
// A live id answers 200 with `<title>Mikan Project - <name>`; a retired id
// answers a redirect to the site root. The read never follows that redirect:
// following it would turn "no such group" into "200 + the site's default
// title", the trap the dataset collection hit. A redirect or a 404 is a
// definitive "does not exist" (found=false with a nil error); every other
// non-2xx answer is an upstream failure and stays an error, so a provider
// outage is never reported as a missing group.
//
// The id is normalized through providerdomain.NewGroupRef before it reaches
// the URL, so only a canonical positive integer is ever requested.
func (r MikanGroupReader) ReadGroupByID(ctx context.Context, groupExternalID string) (providerdomain.GroupEntry, bool, error) {
	ref, err := providerdomain.NewGroupRef(providerdomain.ProviderMikan, groupExternalID)
	if err != nil {
		return providerdomain.GroupEntry{}, false, err
	}
	endpoint, err := providerEndpoint(r.Endpoint, "/Home/PublishGroup/"+ref.ExternalID)
	if err != nil {
		return providerdomain.GroupEntry{}, false, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return providerdomain.GroupEntry{}, false, fmt.Errorf("create Mikan group page request: %w", err)
	}
	request.Header.Set("Accept", "text/html,application/xhtml+xml")
	request.Header.Set("User-Agent", buildinfo.UserAgent("provider"))
	response, err := noRedirectClient(httpClient(r.Client)).Do(request)
	if err != nil {
		return providerdomain.GroupEntry{}, false, fmt.Errorf("fetch Mikan group page: %w", err)
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode == http.StatusOK:
	case response.StatusCode >= 300 && response.StatusCode < 400:
		// A retired id: Mikan answers 302 to the site root.
		return providerdomain.GroupEntry{}, false, nil
	case response.StatusCode == http.StatusNotFound:
		return providerdomain.GroupEntry{}, false, nil
	default:
		return providerdomain.GroupEntry{}, false, fmt.Errorf("Mikan group page returned HTTP %d", response.StatusCode)
	}
	payload, err := readBoundedResponse(response)
	if err != nil {
		return providerdomain.GroupEntry{}, false, fmt.Errorf("read Mikan group page: %w", err)
	}
	document, err := goquery.NewDocumentFromReader(bytes.NewReader(payload))
	if err != nil {
		return providerdomain.GroupEntry{}, false, fmt.Errorf("parse Mikan group page HTML: %w", err)
	}
	title := strings.TrimSpace(document.Find("title").First().Text())
	if !strings.HasPrefix(title, mikanGroupPageTitlePrefix) {
		return providerdomain.GroupEntry{}, false, fmt.Errorf("Mikan group page %s does not carry the %q title marker", ref.ExternalID, mikanGroupPageTitlePrefix)
	}
	name := strings.TrimSpace(strings.TrimPrefix(title, mikanGroupPageTitlePrefix))
	if name == "" || len(name) > maxMikanGroupTitleLength {
		return providerdomain.GroupEntry{}, false, fmt.Errorf("Mikan group page %s has no bounded display name", ref.ExternalID)
	}
	return providerdomain.GroupEntry{Ref: ref, DisplayName: name, Available: true}, true, nil
}

func mikanGroupSchemaVersionFromResponse(document *goquery.Document, headers http.Header) (string, bool) {
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

// mikanGroupTitleAnchorPresent reports whether the bangumi detail page renders
// a recognizable title container. Mikan's `/Home/Bangumi/{id}` page keeps the
// title block even when no subgroup has published, so a missing anchor is a
// strong page-layout-change signal rather than a legitimate empty snapshot.
// The anchors mirror the selectors exercised by the group-reader fixtures.
func mikanGroupTitleAnchorPresent(document *goquery.Document) bool {
	for _, selector := range []string{"h1", ".bangumi-title", ".page-header", "header"} {
		if document.Find(selector).Length() > 0 {
			return true
		}
	}
	return false
}

func recordMikanGroupParserWarning(ctx context.Context, recorder observabilityport.Recorder, catalogEntryRef, errorCode, expectedSchemaVersion, observedSchemaVersion string) {
	if recorder == nil {
		return
	}
	scopeID := "mikan-groups-" + catalogEntryRef
	details, _ := json.Marshal(map[string]string{
		"provider":                "mikan",
		"source":                  "groups",
		"catalog_entry_ref":       catalogEntryRef,
		"response_path":           "Home/Bangumi/" + catalogEntryRef,
		"expected_schema_version": expectedSchemaVersion,
		"observed_schema_version": observedSchemaVersion,
	})
	if err := recorder.RecordDiagnosticFailure(ctx, observabilityport.DiagnosticFailureRequest{
		Identity: observabilityport.ProviderRefreshFailure(observabilityport.DiagnosticErrorCode(errorCode)), Severity: observabilityport.SeverityWarning,
		ScopeID: scopeID, LastError: "Mikan groups parser warning", Details: details,
	}); err != nil {
		observabilityport.RecordDiagnosticError(nil, "provider", "refresh", scopeID, errorCode, "diagnostic_write_failed")
	}
	if err := recorder.UpsertRoutine(ctx, observabilityport.RoutineRequest{
		Identity: observabilityport.ProviderRefreshRoutine(observabilityport.RoutineOutcomeWarning, observabilityport.RoutineResultProviderParserWarning),
		ScopeID:  scopeID, Summary: "Mikan groups parser warning", Details: details,
	}); err != nil {
		observabilityport.RecordRoutineError(nil, "provider_refresh", "provider", scopeID, "routine_write_failed")
	}
}

func closeMikanGroupParserWarning(ctx context.Context, recorder observabilityport.Recorder, catalogEntryRef, errorCode string) {
	if recorder == nil {
		return
	}
	scopeID := "mikan-groups-" + catalogEntryRef
	if err := recorder.RecordDiagnosticSuccess(ctx, observabilityport.DiagnosticSuccessRequest{
		Identity: observabilityport.ProviderRefreshFailure(observabilityport.DiagnosticErrorCode(errorCode)), ScopeID: scopeID,
	}); err != nil {
		observabilityport.RecordDiagnosticError(nil, "provider", "refresh", scopeID, errorCode, "diagnostic_close_failed")
	}
}

func restoreMikanGroupRoutine(ctx context.Context, recorder observabilityport.Recorder, catalogEntryRef string) {
	if recorder == nil {
		return
	}
	scopeID := "mikan-groups-" + catalogEntryRef
	if err := recorder.UpsertRoutine(ctx, observabilityport.RoutineRequest{
		Identity: observabilityport.ProviderRefreshRoutine(observabilityport.RoutineOutcomeSuccess, observabilityport.RoutineResultProviderRefreshCompleted),
		ScopeID:  scopeID, Summary: "Mikan groups parser recovered",
	}); err != nil {
		observabilityport.RecordRoutineError(nil, "provider_refresh", "provider", scopeID, "routine_write_failed")
	}
}
