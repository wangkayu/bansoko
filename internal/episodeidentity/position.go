package episodeidentity

// Position is the shared read projection of a resolved episode identity. Raw
// is the pre-offset evidence, Number and End are final values, Offset is the
// offset actually applied by the resolver, and Source identifies the evidence
// owner. End is empty for a single episode.
//
// Keep this type in the leaf identity package: acquisition, naming, RSS,
// notifications and maintenance all project the same identity without owning
// separate range-display rules.
type Position struct {
	Raw    string
	Number string
	End    string
	Offset int
	Source string
}

// Display renders the canonical human-readable episode position. A multi-
// episode file is "first-last"; a single episode is just its number.
func (p Position) Display() string {
	if p.End != "" && p.End != p.Number {
		return p.Number + "-" + p.End
	}
	return p.Number
}

// Position projects a resolved identity for display and naming. Unresolved or
// malformed identities have no position and return false.
func (identity EpisodeIdentity) Position() (Position, bool) {
	if identity.State != StateResolved || identity.Episode == nil {
		return Position{}, false
	}
	position := Position{Number: string(*identity.Episode), Offset: identity.Offset}
	if identity.RawEpisode != nil {
		position.Raw = string(*identity.RawEpisode)
	}
	if identity.EpisodeEnd != nil {
		position.End = string(*identity.EpisodeEnd)
	}
	if identity.SelectedSource != nil {
		position.Source = string(*identity.SelectedSource)
	}
	return position, true
}

// SameCoverageKeys reports whether a plan and one release identity describe the
// same canonical coverage set. It distinguishes one multi-episode physical file
// from a multi-file batch without comparing presentation order or duplicates.
func SameCoverageKeys(planKeys, identityKeys []string) bool {
	if len(planKeys) == 0 || len(identityKeys) == 0 {
		return false
	}
	identitySet := make(map[string]struct{}, len(identityKeys))
	for _, key := range CanonicalCoverageKeys(identityKeys) {
		identitySet[key] = struct{}{}
	}
	planSet := make(map[string]struct{}, len(planKeys))
	for _, key := range CanonicalCoverageKeys(planKeys) {
		planSet[key] = struct{}{}
	}
	if len(planSet) != len(identitySet) {
		return false
	}
	for key := range planSet {
		if _, ok := identitySet[key]; !ok {
			return false
		}
	}
	return true
}
