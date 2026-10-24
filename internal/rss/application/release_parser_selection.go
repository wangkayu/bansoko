package application

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	acquisitiontarget "bansoko/internal/acquisition/target"
	"bansoko/internal/episodeidentity"
	"bansoko/internal/parser"
	"bansoko/internal/postprocessing/naming"
)

type releaseParserEvidence struct {
	acquisitiontarget.ParserEvidence
}

func (e releaseParserEvidence) namingFacts() *naming.FrozenEpisodeNamingFacts { return e.NamingFacts() }
func (e releaseParserEvidence) provenance() parser.Provenance                 { return e.Provenance() }

func resolveSourceParserEvidence(ctx context.Context, snapshot sourceSnapshot, title string) (releaseParserEvidence, error) {
	evidence, err := acquisitiontarget.ResolveParserEvidence(ctx, acquisitiontarget.ParserSelectionInput{
		Kind: string(parser.ParserKind(strings.TrimSpace(snapshot.parserKind))), EpisodePatterns: snapshot.effective.Rules.Parser.EpisodePatterns,
		Offset: snapshot.episodeOffset, MediaKind: snapshot.mediaKind, Season: snapshot.mediaSeason,
		SeasonBound: snapshot.mediaSeasonBound, SourceVersion: snapshot.sourceVersion,
		PolicyVersion: snapshot.parserVersion, ProfileVersion: snapshot.profileVersion, RulesOwnerVersion: snapshot.rulesVersion,
	}, title)
	if err != nil {
		return releaseParserEvidence{}, err
	}
	return releaseParserEvidence{ParserEvidence: evidence}, nil
}

func parserUnavailableIdentity() episodeidentity.EpisodeIdentity {
	code := "parser_selection_unavailable"
	return episodeidentity.EpisodeIdentity{Kind: episodeidentity.KindTVEpisode, State: episodeidentity.StateUnresolved, Shape: episodeidentity.ShapeUnknown, FailureCode: &code, ResolverVersion: episodeidentity.ResolverVersion}
}

// releaseParserSelection is the parser selection of a release's owner and
// source: nothing in it depends on the release itself, only on the source and
// the owner's configuration, so every release of one source shares it.
func releaseParserSelection(snapshot releaseSnapshot) acquisitiontarget.ParserSelectionInput {
	return acquisitiontarget.ParserSelectionInput{
		Kind: string(selectedParserKind(snapshot)), EpisodePatterns: snapshot.effective.Rules.Parser.EpisodePatterns,
		Offset: snapshot.episodeOffset, MediaKind: snapshot.mediaKind, Season: snapshot.mediaSeason,
		SeasonBound: snapshot.mediaSeasonBound, SourceVersion: snapshot.sourceVersion,
		PolicyVersion: snapshot.parserVersion, ProfileVersion: snapshot.profileVersion, RulesOwnerVersion: snapshot.rulesVersion,
	}
}

// resolveReleaseParserEvidence is the parser selection path used by release_evaluate.
func resolveReleaseParserEvidence(ctx context.Context, snapshot releaseSnapshot, title string) (releaseParserEvidence, error) {
	return ResolveReleaseParserEvidence(ctx, releaseParserSelection(snapshot), title)
}

// ResolveReleaseParserEvidence resolves one title using the authoritative parser implementation.
func ResolveReleaseParserEvidence(ctx context.Context, selection acquisitiontarget.ParserSelectionInput, title string) (releaseParserEvidence, error) {
	evidence, err := acquisitiontarget.ResolveParserEvidence(ctx, selection, title)
	if err != nil {
		return releaseParserEvidence{}, err
	}
	return releaseParserEvidence{ParserEvidence: evidence}, nil
}

// ResolvePersistedReleaseParserEvidence uses the configuration and parser-policy snapshot read by release_evaluate.
func ResolvePersistedReleaseParserEvidence(ctx context.Context, db *sql.DB, releaseID, title string) (acquisitiontarget.ParserEvidence, error) {
	selection, err := LoadPersistedReleaseParserSelection(ctx, db, releaseID)
	if err != nil {
		return acquisitiontarget.ParserEvidence{}, err
	}
	return ResolveParserEvidenceForSelection(ctx, selection, title)
}

// LoadPersistedReleaseParserSelection reads, with the snapshot release_evaluate
// reads, the parser selection of one persisted release's source. A reader that
// resolves many releases of one source loads it once and resolves each title
// with ResolveParserEvidenceForSelection instead of re-reading the owner's
// configuration for every release.
func LoadPersistedReleaseParserSelection(ctx context.Context, db *sql.DB, releaseID string) (acquisitiontarget.ParserSelectionInput, error) {
	if db == nil {
		return acquisitiontarget.ParserSelectionInput{}, errors.New("release parser evidence database is nil")
	}
	snapshot, err := (&Service{db: db}).loadReleaseSnapshot(ctx, releaseID)
	if err != nil {
		return acquisitiontarget.ParserSelectionInput{}, err
	}
	return releaseParserSelection(snapshot), nil
}

// ResolveParserEvidenceForSelection resolves one title with a parser selection
// loaded by LoadPersistedReleaseParserSelection.
func ResolveParserEvidenceForSelection(ctx context.Context, selection acquisitiontarget.ParserSelectionInput, title string) (acquisitiontarget.ParserEvidence, error) {
	evidence, err := ResolveReleaseParserEvidence(ctx, selection, title)
	if err != nil {
		return acquisitiontarget.ParserEvidence{}, err
	}
	return evidence.ParserEvidence, nil
}
