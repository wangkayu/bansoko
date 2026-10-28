package application

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"bansoko/internal/observability/outbound"
	observabilityport "bansoko/internal/observability/port"
)

func downloaderFailure(taskID string) observabilityport.DiagnosticFailureRequest {
	return observabilityport.DiagnosticFailureRequest{
		Condition: observabilityport.Condition{Kind: observabilityport.KindDownloader, Variant: observabilityport.VariantUnreachable},
		Severity:  observabilityport.SeverityError, TargetID: "qb-main", TargetName: "qBittorrent",
		Failure:    outbound.Failure{Host: "qb.local", Class: outbound.ClassConnectionRefused, Via: outbound.ViaDirect},
		AffectedID: taskID,
	}
}

// Three TMDB failures on three anime are one open episode with three
// occurrences and three affected anime; success closes it and a later failure
// opens a second one.
func TestDiagnosticFailuresMergeIntoOneEpisodePerTarget(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	service, err := NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := service.RecordDiagnosticFailure(ctx, observabilityport.DiagnosticFailureRequest{
			Condition: observabilityport.Condition{Kind: observabilityport.KindMetadata, Variant: observabilityport.VariantUnreachable},
			Severity:  observabilityport.SeverityWarning, TargetID: "tmdb", TargetName: "TMDB",
			Failure:    outbound.Failure{Class: outbound.ClassTimeout, Via: outbound.ViaProxy},
			AffectedID: fmt.Sprintf("anime-%d", i),
		}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := service.ListDiagnostics(ctx, DiagnosticQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Episodes) != 1 {
		t.Fatalf("episodes = %d, want one merged episode", len(page.Episodes))
	}
	ep := page.Episodes[0]
	if ep.OccurrenceCount != 3 || ep.Affected == nil || ep.Affected.Count != 3 || ep.Affected.Kind != "anime" || len(ep.Recent) != 3 {
		t.Fatalf("merged episode = %+v", ep)
	}
	if ep.Detail == nil || ep.Detail.ErrorClass != "timeout" || ep.Retry.Mode != "auto" {
		t.Fatalf("detail/retry = %+v / %+v", ep.Detail, ep.Retry)
	}

	if err := service.RecordDiagnosticSuccess(ctx, observabilityport.DiagnosticSuccessRequest{Kind: observabilityport.KindMetadata, TargetID: "tmdb"}); err != nil {
		t.Fatal(err)
	}
	if err := service.RecordDiagnosticFailure(ctx, observabilityport.DiagnosticFailureRequest{
		Condition: observabilityport.Condition{Kind: observabilityport.KindMetadata, Variant: observabilityport.VariantUnreachable},
		Severity:  observabilityport.SeverityWarning, TargetID: "tmdb", TargetName: "TMDB", AffectedID: "anime-9",
	}); err != nil {
		t.Fatal(err)
	}
	page, err = service.ListDiagnostics(ctx, DiagnosticQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Episodes) != 2 || page.Episodes[0].SucceededAfterAt != nil || page.Episodes[1].SucceededAfterAt == nil {
		t.Fatalf("want the open episode first, then the recovered one: %+v", page.Episodes)
	}
}

// N downloader tasks failing against one connection are one episode that
// counts N affected tasks, not N episodes.
func TestDownloaderFailuresCountAffectedTasks(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	service, err := NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	const tasks = 7
	for i := 0; i < tasks; i++ {
		if err := service.RecordDiagnosticFailure(ctx, downloaderFailure(fmt.Sprintf("task-%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	// The same task failing again does not raise the count.
	if err := service.RecordDiagnosticFailure(ctx, downloaderFailure("task-0")); err != nil {
		t.Fatal(err)
	}
	page, err := service.ListDiagnostics(ctx, DiagnosticQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Episodes) != 1 || page.Episodes[0].Affected == nil || page.Episodes[0].Affected.Count != tasks || page.Episodes[0].Affected.Kind != "task" {
		t.Fatalf("episodes = %+v", page.Episodes)
	}
	if page.Episodes[0].OccurrenceCount != tasks+1 {
		t.Fatalf("occurrences = %d", page.Episodes[0].OccurrenceCount)
	}
}

func TestListDiagnosticsStateFilterOrderingAndCursor(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	service, err := NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		req := downloaderFailure("task")
		req.TargetID = fmt.Sprintf("qb-%d", i)
		if err := service.RecordDiagnosticFailure(ctx, req); err != nil {
			t.Fatal(err)
		}
		if i%2 == 0 {
			if err := service.RecordDiagnosticSuccess(ctx, observabilityport.DiagnosticSuccessRequest{Kind: observabilityport.KindDownloader, TargetID: req.TargetID}); err != nil {
				t.Fatal(err)
			}
		}
	}
	limit := 2
	var seen []string
	var cursor *string
	for {
		page, err := service.ListDiagnostics(ctx, DiagnosticQuery{Limit: &limit, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, ep := range page.Episodes {
			seen = append(seen, ep.Target.ID)
		}
		if page.NextCursor == nil {
			break
		}
		cursor = page.NextCursor
	}
	if len(seen) != 5 {
		t.Fatalf("paged episodes = %v, want all five exactly once", seen)
	}
	open, recovered := "open", "recovered"
	openPage, err := service.ListDiagnostics(ctx, DiagnosticQuery{State: &open})
	if err != nil || len(openPage.Episodes) != 2 {
		t.Fatalf("open = %d err=%v", len(openPage.Episodes), err)
	}
	recoveredPage, err := service.ListDiagnostics(ctx, DiagnosticQuery{State: &recovered})
	if err != nil || len(recoveredPage.Episodes) != 3 {
		t.Fatalf("recovered = %d err=%v", len(recoveredPage.Episodes), err)
	}
	bad := "closed"
	if _, err := service.ListDiagnostics(ctx, DiagnosticQuery{State: &bad}); err == nil {
		t.Fatal("unknown state accepted")
	}
}

// Internal errors are never listed as episodes; they fold into one summary on
// the first page that carries the latest opaque request id.
func TestInternalEpisodesFoldIntoSummary(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	service, err := NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, req := range []observabilityport.DiagnosticFailureRequest{
		observabilityport.InternalFailure(observabilityport.InternalHTTP5xx, "request-a"),
		observabilityport.InternalFailure(observabilityport.InternalHTTP5xx, "request-b"),
		observabilityport.InternalFailure(observabilityport.InternalPlacementSink, ""),
	} {
		if err := service.RecordDiagnosticFailure(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	page, err := service.ListDiagnostics(ctx, DiagnosticQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Episodes) != 0 || page.Internal == nil || page.Internal.OccurrenceCount != 3 {
		t.Fatalf("page = %+v internal=%+v", page.Episodes, page.Internal)
	}
	if page.Internal.LastRequestID == nil || *page.Internal.LastRequestID != "request-b" {
		t.Fatalf("last request id = %v", page.Internal.LastRequestID)
	}
	if err := service.RecordDiagnosticSuccess(ctx, observabilityport.InternalSuccess(observabilityport.InternalHTTP5xx)); err != nil {
		t.Fatal(err)
	}
	if err := service.RecordDiagnosticSuccess(ctx, observabilityport.InternalSuccess(observabilityport.InternalPlacementSink)); err != nil {
		t.Fatal(err)
	}
	page, err = service.ListDiagnostics(ctx, DiagnosticQuery{})
	if err != nil || page.Internal != nil {
		t.Fatalf("closed internal sources must leave no summary: %+v err=%v", page.Internal, err)
	}
}

// Acceptance: no free text and no secret-shaped input reaches the stored row
// or the listed episode. Sensitive text can only enter through the target name
// and the host, both of which are validated or masked.
func TestDiagnosticRowsHoldNoFreeText(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	service, err := NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	req := downloaderFailure("task-1")
	req.TargetName = "qb https://user:pass@qb.local/api?token=abcdef0123456789"
	if err := service.RecordDiagnosticFailure(ctx, req); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := db.QueryRowContext(ctx, `SELECT row_to_json(e)::text FROM diagnostic_episodes e`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"user:pass", "abcdef0123456789", "token="} {
		if strings.Contains(stored, forbidden) {
			t.Fatalf("stored row leaked %q: %s", forbidden, stored)
		}
	}
	for _, column := range []string{"last_error", "details", "error_code", "scope_id", "managed_anime_id"} {
		if strings.Contains(stored, `"`+column+`"`) {
			t.Fatalf("retired column %q is back: %s", column, stored)
		}
	}
	bad := downloaderFailure("task-2")
	bad.Failure.Host = "qb.local/private/path"
	if err := service.RecordDiagnosticFailure(ctx, bad); err != nil {
		t.Fatalf("a malformed host must cost only the host: %v", err)
	}
	var host sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT target_host FROM diagnostic_episodes WHERE kind='downloader' AND succeeded_after_at IS NULL`).Scan(&host); err != nil {
		t.Fatal(err)
	}
	if host.Valid {
		t.Fatalf("a host carrying a path was stored: %q", host.String)
	}
}
