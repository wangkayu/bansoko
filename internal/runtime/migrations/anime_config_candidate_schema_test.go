package migrations

import "testing"

// Candidate drafts are digest-bound, move through working, dormant and
// archived, and only one working draft exists per provider entry.
func TestAnimeConfigCandidateSchema(t *testing.T) {
	requireFragments(t, baselineText(t), "anime config candidate schema",
		"draft_digest TEXT",
		"last_activity TIMESTAMPTZ",
		"CREATE FUNCTION anime_config_candidate_canonical_json",
		"candidate draft digest does not match canonical draft",
		"status IN ('working', 'dormant', 'archived')",
		"anime_config_candidates_lifecycle_check",
		"CREATE UNIQUE INDEX anime_config_candidates_working_provider_uq",
		"WHERE status = 'working'",
	)
}
