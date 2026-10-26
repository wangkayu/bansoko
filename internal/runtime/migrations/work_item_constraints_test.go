package migrations

import "testing"

// At most one live work item exists per owner and kind; state, retention and
// the owner/kind pairing are closed sets.
func TestWorkItemConstraints(t *testing.T) {
	p := newConstraintProbe(t)
	seedRSSSubscription(p)
	p.accept("rss source", `
INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title)
VALUES ('src-1', 'sub-1', 'custom_rss', 'custom://work', 'custom://work', 'Custom', 'Title')`)
	p.accept("runtime row", `INSERT INTO rss_source_runtime(rss_source_id) VALUES ('src-1')`)

	workItem := `INSERT INTO work_items(id, owner_type, owner_id, kind, idempotency_key, max_attempts, input_snapshot, expected_owner_version)
		VALUES ($1, 'rss_source_runtime', 'src-1', 'rss_refresh', $2, 3, '{}', 1)`
	p.accept("first work item", workItem, "wi-1", "effect-1")
	p.reject("second live work item for the same owner and kind", workItem, "wi-2", "effect-2")
	p.reject("invalid work item state",
		`INSERT INTO work_items(id, owner_type, owner_id, kind, state, idempotency_key, max_attempts, input_snapshot, expected_owner_version)
		 VALUES ('wi-3', 'rss_source_runtime', 'src-1', 'rss_refresh', 'waiting_retry', 'effect-3', 3, '{}', 1)`)
	p.reject("retention above the 30-day hard bound",
		`INSERT INTO work_items(id, owner_type, owner_id, kind, idempotency_key, max_attempts, input_snapshot, expected_owner_version, retention_days)
		 VALUES ('wi-4', 'rss_source_runtime', 'src-1', 'rss_refresh', 'effect-4', 3, '{}', 1, 31)`)
	p.reject("owner/kind mismatch",
		`INSERT INTO work_items(id, owner_type, owner_id, kind, idempotency_key, max_attempts, input_snapshot, expected_owner_version)
		 VALUES ('wi-5', 'rss_source_runtime', 'src-1', 'qb_submit', 'effect-5', 3, '{}', 1)`)

	// A terminal row does not block a new live item for the same owner/kind.
	p.accept("terminal transition", `
		SELECT public.work_item_close(id,'failed') FROM work_items
WHERE id = 'wi-1'`)
	p.accept("new live item after terminal", workItem, "wi-6", "effect-1")
}

// The owner/kind constraint has a branch for every owner type, including the
// offset migration operation and replacement evaluation shapes.
func TestWorkItemOwnerShapeCoversEveryOwnerType(t *testing.T) {
	db := installedDB(t)
	var definitions string
	if err := db.QueryRow(`SELECT string_agg(pg_get_constraintdef(oid), ' ') FROM pg_constraint WHERE conrelid = 'work_items'::regclass`).Scan(&definitions); err != nil {
		t.Fatal(err)
	}
	fragments := []string{
		"owner_type = 'offset_migration_operation'",
		"kind = 'offset_migration_execute'",
		"owner_type = 'file_maintenance'",
		"'replacement_evaluate'::text",
	}
	for _, ownerType := range []string{"rss_source_runtime", "release", "download_task", "artifact", "file_operation", "plugin_delivery", "file_maintenance", "alignment_operation", "replacement_deletion"} {
		fragments = append(fragments, "owner_type = '"+ownerType+"'")
	}
	requireFragments(t, definitions, "work item owner shape", fragments...)
}
