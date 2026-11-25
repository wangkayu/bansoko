package varspreflight

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"

	"bansoko/internal/postprocessing/naming"
	"bansoko/internal/templatecatalog/notification"
)

// Hit identifies one editable template field that still references naming Vars.
type Hit struct {
	Location string `json:"location"`
	Field    string `json:"field"`
}

func (h Hit) String() string {
	return h.Location + " (" + h.Field + ")"
}

// Queryer is shared by the diagnostic connection and the migration transaction.
type Queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// Blocked reports identifiers and field names, not the stored template text.
type Blocked struct{ Hits []Hit }

func (e *Blocked) Error() string {
	return fmt.Sprintf("naming Vars still used in %d template field(s); run `bansoko-server migrate check-vars` to locate them", len(e.Hits))
}

// Scan inspects editable templates and unfinished acquisitions that still need
// to render targets. Results are sorted independently of database row order.
func Scan(ctx context.Context, db Queryer) ([]Hit, error) {
	var hits []Hit

	// 1. Global naming_rules.
	var globalRaw []byte
	err := db.QueryRowContext(ctx, `SELECT value FROM global_config_sections WHERE section_name = 'naming_rules' AND value IS NOT NULL`).Scan(&globalRaw)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("scan global naming_rules: %w", err)
	}
	if len(globalRaw) > 0 {
		found, scanErr := ScanNamingRulesJSON("global_config_sections:naming_rules", globalRaw)
		if scanErr != nil {
			return nil, fmt.Errorf("scan global naming_rules: %w", scanErr)
		}
		hits = append(hits, found...)
	}

	// 2. Naming template library.
	var libraryRaw []byte
	err = db.QueryRowContext(ctx, `SELECT value FROM naming_template_library WHERE singleton`).Scan(&libraryRaw)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("scan naming_template_library: %w", err)
	}
	if len(libraryRaw) > 0 {
		found, scanErr := ScanNamingTemplateLibraryJSON(libraryRaw)
		if scanErr != nil {
			return nil, fmt.Errorf("scan naming_template_library: %w", scanErr)
		}
		hits = append(hits, found...)
	}

	// 3. Per-anime naming_rules overrides.
	if err := scanRows(ctx, db, `SELECT managed_anime_id, value FROM anime_config_overrides WHERE section_name = 'naming_rules' AND value IS NOT NULL ORDER BY managed_anime_id`, func(id string, raw []byte) error {
		found, err := ScanNamingRulesJSON("anime_config_overrides:"+id, raw)
		hits = append(hits, found...)
		return err
	}); err != nil {
		return nil, fmt.Errorf("scan anime_config_overrides: %w", err)
	}

	// 4. Owner business snapshots (active anime).
	if err := scanRows(ctx, db, `SELECT managed_anime_id, business_snapshot FROM anime_business_configs WHERE business_snapshot IS NOT NULL ORDER BY managed_anime_id`, func(id string, raw []byte) error {
		found, err := ScanBusinessSnapshotJSON("anime_business_configs:"+id, raw)
		hits = append(hits, found...)
		return err
	}); err != nil {
		return nil, fmt.Errorf("scan anime_business_configs: %w", err)
	}

	// 5. Working (non-linked) candidate drafts.
	if err := scanRows(ctx, db, `SELECT id, draft FROM anime_config_candidates WHERE activated_managed_anime_id IS NULL AND status = 'working' ORDER BY id`, func(id string, raw []byte) error {
		found, err := ScanCandidateDraftJSON("anime_config_candidates:"+id, raw)
		hits = append(hits, found...)
		return err
	}); err != nil {
		return nil, fmt.Errorf("scan anime_config_candidates: %w", err)
	}

	// 6. Revisions of working (non-linked) candidates.
	revRows, err := db.QueryContext(ctx, `SELECT r.candidate_id, r.revision, r.draft, r.business_snapshot
FROM anime_config_candidate_revisions r
JOIN anime_config_candidates c ON c.id = r.candidate_id
WHERE c.activated_managed_anime_id IS NULL AND c.status = 'working'
ORDER BY r.candidate_id, r.revision`)
	if err != nil {
		return nil, fmt.Errorf("scan anime_config_candidate_revisions: %w", err)
	}
	for revRows.Next() {
		var candidateID string
		var revision int64
		var draftRaw, snapshotRaw []byte
		if err := revRows.Scan(&candidateID, &revision, &draftRaw, &snapshotRaw); err != nil {
			_ = revRows.Close()
			return nil, fmt.Errorf("scan candidate revision row: %w", err)
		}
		prefix := fmt.Sprintf("anime_config_candidate_revisions:%s@%d", candidateID, revision)
		if len(draftRaw) > 0 {
			found, scanErr := ScanCandidateDraftJSON(prefix+":draft", draftRaw)
			if scanErr != nil {
				_ = revRows.Close()
				return nil, fmt.Errorf("scan candidate revision draft: %w", scanErr)
			}
			hits = append(hits, found...)
		}
		if len(snapshotRaw) > 0 {
			found, scanErr := ScanBusinessSnapshotJSON(prefix+":business_snapshot", snapshotRaw)
			if scanErr != nil {
				_ = revRows.Close()
				return nil, fmt.Errorf("scan candidate revision business snapshot: %w", scanErr)
			}
			hits = append(hits, found...)
		}
	}
	if err := revRows.Err(); err != nil {
		_ = revRows.Close()
		return nil, fmt.Errorf("iterate candidate revisions: %w", err)
	}
	_ = revRows.Close()

	// 7. Active notification plugin target templates.
	if err := scanRows(ctx, db, `SELECT id, template_set FROM plugin_target_configs WHERE deleted_at IS NULL ORDER BY id`, func(id string, raw []byte) error {
		found, err := ScanNotificationTemplateSetJSON("plugin_target_configs:"+id, raw)
		hits = append(hits, found...)
		return err
	}); err != nil {
		return nil, fmt.Errorf("scan plugin_target_configs: %w", err)
	}

	// A plan waiting for configuration still belongs to a running acquisition.
	// Sealed targets already hold their rendered paths and do not need Vars.
	if err := scanRows(ctx, db, `SELECT id, input FROM public.acquisitions
WHERE state = 'running' AND record_state = 'full' AND target_sealed_at IS NULL ORDER BY id`, func(id string, raw []byte) error {
		found, err := scanFrozenInput("acquisitions:"+id, raw)
		hits = append(hits, found...)
		return err
	}); err != nil {
		return nil, fmt.Errorf("scan unfinished acquisitions: %w", err)
	}

	sortHits(hits)
	return hits, nil
}

func scanFrozenInput(location string, raw []byte) ([]Hit, error) {
	var input struct {
		Config struct {
			Sections []struct {
				Key   string          `json:"section_key"`
				Value json.RawMessage `json:"value"`
			} `json:"sections"`
		} `json:"config"`
		Delivery struct {
			Naming struct {
				Templates json.RawMessage `json:"templates"`
			} `json:"naming"`
		} `json:"delivery"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, fmt.Errorf("%s: decode frozen input: %w", location, err)
	}
	var hits []Hit
	for _, section := range input.Config.Sections {
		if section.Key != "naming_rules" {
			continue
		}
		found, err := ScanNamingRulesJSON(location+":config.sections.naming_rules", section.Value)
		if err != nil {
			return nil, err
		}
		hits = append(hits, found...)
	}
	found, err := ScanNamingRulesJSON(location+":delivery.naming.templates", input.Delivery.Naming.Templates)
	if err != nil {
		return nil, err
	}
	return append(hits, found...), nil
}

func scanRows(ctx context.Context, db Queryer, query string, visit func(id string, raw []byte) error) error {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return err
		}
		if len(raw) > 0 {
			if err := visit(id, raw); err != nil {
				return err
			}
		}
	}
	return rows.Err()
}

// ScanNamingRulesJSON inspects one serialized NamingRules JSON document.
// Invalid JSON and unparseable templates are errors, never clean results.
func ScanNamingRulesJSON(location string, raw []byte) ([]Hit, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var rules naming.NamingRules
	if err := json.Unmarshal(raw, &rules); err != nil {
		return nil, fmt.Errorf("%s: decode naming rules: %w", location, err)
	}
	fields, err := naming.RulesVarsReferences(rules)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", location, err)
	}
	if len(fields) == 0 {
		return nil, nil
	}
	hits := make([]Hit, 0, len(fields))
	for _, field := range fields {
		hits = append(hits, Hit{Location: location, Field: field})
	}
	return hits, nil
}

// ScanNamingTemplateLibraryJSON inspects every non-default entry in a
// naming_template_library JSON document.
func ScanNamingTemplateLibraryJSON(raw []byte) ([]Hit, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var lib struct {
		Templates []struct {
			Key   string          `json:"key"`
			Rules json.RawMessage `json:"rules"`
		} `json:"templates"`
	}
	if err := json.Unmarshal(raw, &lib); err != nil {
		return nil, fmt.Errorf("naming_template_library: decode library: %w", err)
	}
	var hits []Hit
	for _, entry := range lib.Templates {
		if len(entry.Rules) == 0 || string(entry.Rules) == "null" {
			continue
		}
		found, err := ScanNamingRulesJSON("naming_template_library:"+entry.Key, entry.Rules)
		if err != nil {
			return nil, err
		}
		hits = append(hits, found...)
	}
	sortHits(hits)
	return hits, nil
}

// ScanBusinessSnapshotJSON inspects sections.naming_rules inside a business
// snapshot JSON document.
func ScanBusinessSnapshotJSON(location string, raw []byte) ([]Hit, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var snap struct {
		Sections struct {
			NamingRules json.RawMessage `json:"naming_rules"`
		} `json:"sections"`
	}
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, fmt.Errorf("%s: decode business snapshot: %w", location, err)
	}
	return ScanNamingRulesJSON(location, snap.Sections.NamingRules)
}

// ScanCandidateDraftJSON inspects business.sections.naming_rules inside a
// candidate draft JSON document. Drafts without a business body return no hits.
func ScanCandidateDraftJSON(location string, raw []byte) ([]Hit, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var draft struct {
		Business json.RawMessage `json:"business"`
	}
	if err := json.Unmarshal(raw, &draft); err != nil {
		return nil, fmt.Errorf("%s: decode candidate draft: %w", location, err)
	}
	if len(draft.Business) == 0 || string(draft.Business) == "null" {
		return nil, nil
	}
	return ScanBusinessSnapshotJSON(location, draft.Business)
}

// ScanNotificationTemplateSetJSON inspects every custom event template in a
// plugin_target_configs.template_set JSON document.
func ScanNotificationTemplateSetJSON(location string, raw []byte) ([]Hit, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var set struct {
		Events map[string]struct {
			Mode          string `json:"mode"`
			TitleTemplate string `json:"title_template"`
			BodyTemplate  string `json:"body_template"`
		} `json:"events"`
	}
	if err := json.Unmarshal(raw, &set); err != nil {
		return nil, fmt.Errorf("%s: decode notification template set: %w", location, err)
	}
	triggers := make([]string, 0, len(set.Events))
	for trigger := range set.Events {
		triggers = append(triggers, trigger)
	}
	sort.Strings(triggers)
	var hits []Hit
	for _, trigger := range triggers {
		event := set.Events[trigger]
		if event.Mode != "custom" {
			continue
		}
		for _, item := range []struct {
			field  string
			source string
		}{
			{"events." + trigger + ".title_template", event.TitleTemplate},
			{"events." + trigger + ".body_template", event.BodyTemplate},
		} {
			found, err := notification.CheckTemplateReferencesVars(item.source)
			if err != nil {
				return nil, fmt.Errorf("%s: %s: %w", location, item.field, err)
			}
			if found {
				hits = append(hits, Hit{Location: location, Field: item.field})
			}
		}
	}
	sortHits(hits)
	return hits, nil
}

func sortHits(hits []Hit) {
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Location != hits[j].Location {
			return hits[i].Location < hits[j].Location
		}
		return hits[i].Field < hits[j].Field
	})
}
