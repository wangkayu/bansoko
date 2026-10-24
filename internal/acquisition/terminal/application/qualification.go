package application

import (
	"encoding/json"
	"errors"
	"fmt"

	protocol "bansoko/internal/acquisition/terminal/domain"
	anime "bansoko/internal/anime/domain"
	"bansoko/internal/attrrules"
	catalog "bansoko/internal/catalog/domain"
	"bansoko/internal/namingvars"
)

var errQualificationMaterial = errors.New("qualification material unavailable")

// qualificationMaterialError identifies the fixed material group without leaking
// database errors or turning unavailable capture into a null qualification claim.
type qualificationMaterialError struct {
	group string
	cause error
}

func (e *qualificationMaterialError) Error() string {
	return "qualification material unavailable: " + e.group
}
func (e *qualificationMaterialError) Unwrap() error        { return e.cause }
func (e *qualificationMaterialError) Is(target error) bool { return target == errQualificationMaterial }
func materialError(group string, cause error) error {
	return &qualificationMaterialError{group: group, cause: cause}
}

type configMaterial struct {
	kind, requestHash, managedID, candidateID, revisionDigest string
	version, revision                                         int64
	business                                                  anime.BusinessSnapshot
	digest                                                    string
}
type managementMaterial struct {
	managedID, subscriptionID             string
	subscriptionVersion, lifecycleVersion int64
	// title, titleOverride and providerTitle are the managed anime's own
	// fields, read with the same row. titleOverride and providerTitle are
	// trimmed to "" when NULL.
	title, titleOverride, providerTitle string
}
type releaseMaterial struct {
	id, sourceID, subscriptionID string
	version, sourceVersion       int64
	// groupName is the release's source group display name, read from the same
	// rss_sources row as sourceVersion. It is the fact every naming render reads
	// back as anime.group, so the accepted input freezes it instead of the
	// display-blind provider group identity.
	groupName string
	// offset is the release's RSS link episode offset, read with the link's
	// version. A one-shot placeholder link carries the input's own offset.
	offset int
}
type catalogMaterial struct {
	observation catalog.GroupObservation
	feed        catalog.ProviderFeedSnapshot
	seedKey     string
}
type programMaterial struct {
	attr       attrrules.Rules
	vars       namingvars.Program
	varsSource string
}

// admissionCapture is backend-only, immutable after construction, and has no
// JSON assembly surface. B1 will consume it inside this package, never transport.
type admissionCapture struct {
	qualification                            protocol.Qualification
	configDigest, requestHash, payloadDigest string
	// Canonical bytes are immutable package-owned material for A4; they never
	// become caller-assembled qualification or transport fields.
	policyCanonical, observationCanonical string
	configKind                            string
	policyVersion                         int64
	businessJSON                          string
	material                              qualificationMaterial
}
type qualificationMaterial struct {
	entryMode      string
	direct         *directMaterial
	managementNone bool
	config         configMaterial
	management     managementMaterial
	release        releaseMaterial
	payloadDigest  string
	catalogKind    string
	catalog        *catalogMaterial // nil is the explicitly chosen direct-release path.
	programs       programMaterial
	bgm            *anime.BGMBinding
	tmdb           *anime.TMDBBinding
	// mediaKindOverride is the owner pin of the naming branch (tv | movie). It
	// is a separate field from the TMDB tuple: the tuple stays exactly what the
	// matcher wrote and the override only selects the frozen media kind.
	mediaKindOverride *string
	// animeYear is the owner-edited naming year (anime.year); nil when unset.
	// It comes from the same source as the binding (saved or submitted draft,
	// or the owner's metadata row) and is frozen in the Vars input.
	animeYear *int
	// animeSeason is the owner-edited naming season (anime.season; 0 =
	// specials); nil when unset. Same source as animeYear, frozen in the Vars
	// input; independent of the TMDB binding's season.
	animeSeason *int
	// bgmTitles / tmdbTitles are the recorded native titles of the bound
	// works (metadata_work_titles); nil when the work was never observed.
	bgmTitles  *workTitles
	tmdbTitles *workTitles
	// releaseSourceFacts is the release's frozen source_facts (rt / anibt
	// value namespaces for the Vars input); nil when the release has none.
	releaseSourceFacts    []byte
	metadataVersion       int64
	parserKind            string
	parserVersion         int64
	rulesOwnerVersion     int64
	ownerResolution       *OwnerResolution
	subgroupsOwnerVersion int64
}

func constructQualification(m qualificationMaterial) (admissionCapture, error) {
	bad := func(group string, cause error) (admissionCapture, error) {
		return admissionCapture{}, materialError(group, cause)
	}
	if (m.catalogKind != "provider_snapshot" && m.catalogKind != "release_input") || (m.catalogKind == "provider_snapshot") != (m.catalog != nil) {
		return bad("catalog", nil)
	}
	digest, err := m.config.business.Digest()
	if err != nil || digest != m.config.digest {
		return bad("config", err)
	}
	if m.config.kind == "owner" && m.ownerResolution != nil {
		digest, err = m.ownerResolution.resolved.EffectiveDigest()
		if err != nil || digest != m.ownerResolution.digest {
			return bad("config", err)
		}
	}
	mg := m.management
	if mg.managedID == "" || mg.subscriptionID == "" || mg.subscriptionVersion < 1 || mg.lifecycleVersion < 1 {
		return bad("management", nil)
	}
	r := m.release
	if r.id == "" || r.sourceID == "" || r.version < 1 || r.sourceVersion < 1 || r.subscriptionID != mg.subscriptionID {
		return bad("source", nil)
	}
	config := map[string]any{"kind": m.config.kind}
	switch m.config.kind {
	case "owner":
		if m.config.managedID != mg.managedID || m.config.version < 1 {
			return bad("config", nil)
		}
		config["managed_anime_ref"], config["config_version"], config["config_digest"] = mg.managedID, m.config.version, digest
	case "saved_revision":
		if m.config.candidateID == "" || m.config.version < 1 || m.config.revision < 1 || m.config.revisionDigest == "" {
			return bad("config", nil)
		}
		config["candidate_ref"], config["candidate_version"], config["revision"], config["revision_digest"] = m.config.candidateID, m.config.version, m.config.revision, m.config.revisionDigest
	case "submitted":
		if m.config.requestHash == "" {
			return bad("config", nil)
		}
		config["request_hash"], config["config_digest"] = m.config.requestHash, digest
	default:
		return bad("config", nil)
	}
	attrDigest, err := attrrules.Fingerprint(m.programs.attr)
	if err != nil || attrDigest != m.config.business.Programs.AttrRules.Digest || m.programs.vars.Ref() != m.config.business.Programs.Vars {
		return bad("programs", err)
	}
	var observation, feed any
	payloadDigest := m.payloadDigest
	if c := m.catalog; c != nil {
		if !c.observation.Authoritative() {
			return bad("observation", nil)
		}
		_, obsDigest, err := c.observation.Canonical()
		if err != nil {
			return bad("observation", err)
		}
		f := c.feed
		if f.VerifyContentHash() != nil || f.ID == "" || f.Version < 1 || f.HashSchema != catalog.ProviderFeedSnapshotContentHashSchema || f.ProjectionSchema != catalog.TorrentManifestProjectionSchema {
			return bad("feed", nil)
		}
		if string(c.observation.Scope.Provider) != f.Provider || c.observation.Scope.CatalogEntryRef != f.ProviderEntryRef {
			return bad("observation", nil)
		}
		available := false
		for _, g := range c.observation.Groups {
			if g.GroupRef == f.ProviderGroupRef && g.Available.Value != nil && *g.Available.Value {
				available = true
			}
		}
		if !available {
			return bad("observation", nil)
		}
		var seed *catalog.ProviderFeedSnapshotSeed
		for i := range f.Seeds {
			if f.Seeds[i].StableSeedKey == c.seedKey {
				if seed != nil {
					return bad("feed", nil)
				}
				seed = &f.Seeds[i]
			}
		}
		if seed == nil || seed.TorrentState != catalog.TorrentAcquisitionAvailable || seed.PayloadDigest == nil || seed.Manifest == nil || seed.Manifest.PayloadDigest != *seed.PayloadDigest {
			return bad("feed", nil)
		}
		if err := seed.Manifest.Validate(); err != nil {
			return bad("feed", err)
		}
		payloadDigest = *seed.PayloadDigest
		scope := map[string]any{"provider": f.Provider, "catalog_entry_ref": f.ProviderEntryRef}
		observation = map[string]any{"scope": scope, "snapshot_ref": c.observation.SnapshotRef, "snapshot_version": c.observation.SnapshotVersion, "observation_digest": obsDigest}
		feed = map[string]any{"scope": scope, "snapshot_ref": f.ID, "snapshot_version": f.Version, "group_ref": f.ProviderGroupRef, "content_hash": f.ContentHash, "hash_schema": f.HashSchema, "projection_schema": f.ProjectionSchema, "stable_seed_key": c.seedKey, "payload_digest": payloadDigest, "manifest_digest": seed.Manifest.ProjectionDigest}
	}
	if !validMaterialDigest(m.config.requestHash) || !validMaterialDigest(payloadDigest) {
		return bad("request", nil)
	}
	raw, err := json.Marshal(map[string]any{
		"config":      config,
		"management":  managementQualificationValue(m),
		"source":      map[string]any{"kind": "release", "release_ref": r.id, "expected_version": r.version, "source_ref": map[string]any{"ref": r.sourceID, "version": r.sourceVersion}},
		"observation": observation, "feed": feed,
		"programs":  map[string]any{"attr_fingerprint": attrDigest, "vars_digest": m.programs.vars.Ref().Digest, "translation": nil},
		"placement": map[string]any{"kind": "none"},
	})
	if err != nil {
		return bad("qualification", err)
	}
	q, err := protocol.DecodeQualification(raw)
	if err != nil {
		return bad("qualification", fmt.Errorf("validate capture: %w", err))
	}
	sections := captureSections(m)
	policy, err := canonicalPolicy(sections.Sections[anime.SectionSubgroups])
	if err != nil {
		return bad("policy", err)
	}
	var observationCanonical []byte
	if m.catalog != nil {
		observationCanonical, _, err = m.catalog.observation.Canonical()
		if err != nil {
			return bad("observation", err)
		}
	}
	ownedBusiness, err := m.config.business.Freeze()
	if err != nil {
		return bad("config", err)
	}
	businessRaw, err := json.Marshal(ownedBusiness)
	if err != nil {
		return bad("config", err)
	}
	return admissionCapture{qualification: q, configDigest: digest, requestHash: m.config.requestHash, payloadDigest: payloadDigest, policyCanonical: policy, observationCanonical: string(observationCanonical), configKind: m.config.kind, policyVersion: sections.Versions[anime.SectionSubgroups], businessJSON: string(businessRaw), material: m}, nil
}

func validMaterialDigest(s string) bool {
	if len(s) != 71 || s[:7] != "sha256:" {
		return false
	}
	for _, c := range s[7:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
