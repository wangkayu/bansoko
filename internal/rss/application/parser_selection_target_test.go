package application

import (
	"bansoko/internal/batchplan"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/acquisition/target"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/namingvars"
	"bansoko/internal/parser"
	"bansoko/internal/persistence/txscope"
	facts "bansoko/internal/release/facts"
)

// recordingParserAdapter is a non-default registered parser used to prove the
// selected kind/profile reaches naming and target planning. It records every
// input it was asked to parse, so a test can show which parser ran and on which
// input, and it deliberately returns an episode the bundled defaults would not.
type recordingParserAdapter struct {
	kind    string
	episode string

	mu    sync.Mutex
	texts []string
}

func (a *recordingParserAdapter) Descriptor() parser.ParserDescriptor {
	return parser.ParserDescriptor{
		ID:                    a.kind,
		ImplementationVersion: "test-selected-parser-v1",
		OutputContractVersion: "episode-evidence-v1",
		DisplayName:           "Test selected parser",
		Capabilities:          []string{"release_title", "file_name", "decimal"},
		ConfigVersion:         "test-selected-config-v1",
	}
}

func (a *recordingParserAdapter) ValidateConfig(json.RawMessage) error { return nil }

func (a *recordingParserAdapter) ParseDraft(_ context.Context, input parser.EpisodeParseInput, _ json.RawMessage) (parser.EpisodeEvidenceDraft, error) {
	a.mu.Lock()
	a.texts = append(a.texts, input.Text)
	a.mu.Unlock()
	if strings.TrimSpace(input.Text) == "" {
		return parser.EpisodeEvidenceDraft{Status: parser.StatusMissing, Shape: parser.ShapeUnknown}, nil
	}
	return parser.EpisodeEvidenceDraft{
		Status: parser.StatusValue, Shape: parser.ShapeSingle,
		RawCapture: a.episode, Episode: a.episode,
	}, nil
}

// parsedInputs returns the inputs this adapter was asked to parse.
func (a *recordingParserAdapter) parsedInputs() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.texts...)
}

// registerRecordingParser registers one test-only parser kind in the
// authoritative registry the production selection validates against. Kinds are
// unique per test so registration never collides.
func registerRecordingParser(t *testing.T, kind, episode string) *recordingParserAdapter {
	t.Helper()
	adapter := &recordingParserAdapter{kind: kind, episode: episode}
	if parser.IsRegistered(parser.ParserKind(kind)) {
		t.Fatalf("test parser kind %q is already registered", kind)
	}
	if err := parser.DefaultRegistry().Register(adapter); err != nil {
		t.Fatalf("register test parser %q: %v", kind, err)
	}
	return adapter
}

type missingTargetProbe struct{}

func (missingTargetProbe) Observe(spec target.Spec) target.Observation {
	return target.Observation{TargetID: spec.ID, State: target.ObservationMissing, ReasonCode: "target_missing"}
}

// targetPlanningSnapshot builds one instance snapshot whose selected parser is
// a non-default registered test kind with a profile the bundled defaults would
// never produce. linkOffset is the RSS link's own episode_offset — the one
// offset authority.
func targetPlanningSnapshot(t *testing.T, kind, pattern string, linkOffset int) releaseSnapshot {
	t.Helper()
	parserRules := animedomain.ParserRules{EpisodePatterns: []string{pattern}}
	resolved := defaultResolvedSections(t)
	parserRaw, err := json.Marshal(parserRules)
	if err != nil {
		t.Fatalf("encode parser rules: %v", err)
	}
	resolved.Sections[animedomain.SectionParser] = parserRaw
	resolved.Versions[animedomain.SectionParser] = 7
	return releaseSnapshot{
		releaseID:      "release-selected-parser",
		sourceID:       "source-selected-parser",
		subscriptionID: "subscription-selected-parser",
		managedAnimeID: "anime-selected-parser",
		animeTitle:     "Preview Show",
		groupName:      "RealGroup",
		parserKind:     kind,
		parserVersion:  11,
		episodeOffset:  linkOffset,
		profileVersion: 7,
		resolved:       resolved,
		effective: animedomain.EffectiveConfig{
			Rules: animedomain.EffectiveRules{
				Parser: parserRules,
				Naming: defaultNamingRules(t),
			},
			Versions: map[string]int64{animedomain.SectionParser: 7},
		},
	}
}

func defaultResolvedSections(t *testing.T) animedomain.ResolvedSections {
	t.Helper()
	resolved := animedomain.ResolvedSections{
		Sections:       map[string]json.RawMessage{},
		Versions:       map[string]int64{},
		SchemaVersions: map[string]string{},
	}
	for _, section := range animedomain.EffectiveSectionOrder {
		value, err := animedomain.DefaultSectionValue(section)
		if err != nil {
			t.Fatalf("default section %s: %v", section, err)
		}
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("encode default section %s: %v", section, err)
		}
		resolved.Sections[section] = raw
		resolved.Versions[section] = 1
		resolved.SchemaVersions[section] = animedomain.SectionSchemaVersions[section]
	}
	return resolved
}

func defaultNamingRules(t *testing.T) animedomain.NamingRules {
	t.Helper()
	value, err := animedomain.DefaultSectionValue(animedomain.SectionNamingRules)
	if err != nil {
		t.Fatalf("default naming section: %v", err)
	}
	rules, ok := value.(animedomain.NamingRules)
	if !ok {
		t.Fatalf("default naming section has type %T", value)
	}
	return rules
}

func TestExportedReleaseParserEvidenceMatchesAutomaticSelection(t *testing.T) {
	const kind = "test-exported-release-parser-evidence"
	registerRecordingParser(t, kind, "42")
	snapshot := targetPlanningSnapshot(t, kind, `\\[E(?P<episode>\\d{1,4})\\]`, 3)
	item := "[RealGroup] Preview Show [E07]"
	automatic, err := resolveReleaseParserEvidence(context.Background(), snapshot, item)
	if err != nil {
		t.Fatal(err)
	}
	projected, err := ResolveReleaseParserEvidence(context.Background(), target.ParserSelectionInput{Kind: kind, EpisodePatterns: snapshot.effective.Rules.Parser.EpisodePatterns, Offset: snapshot.episodeOffset, MediaKind: snapshot.mediaKind, Season: snapshot.mediaSeason, SeasonBound: snapshot.mediaSeasonBound, SourceVersion: snapshot.sourceVersion, PolicyVersion: snapshot.parserVersion, ProfileVersion: snapshot.profileVersion, RulesOwnerVersion: snapshot.rulesVersion}, item)
	if err != nil {
		t.Fatal(err)
	}
	automaticJSON, err := json.Marshal(automatic.ParserEvidence)
	if err != nil {
		t.Fatal(err)
	}
	projectedJSON, err := json.Marshal(projected.ParserEvidence)
	if err != nil {
		t.Fatal(err)
	}
	if string(automaticJSON) != string(projectedJSON) {
		t.Fatalf("exported release parser evidence differs from release_evaluate: automatic=%s projected=%s", automaticJSON, projectedJSON)
	}
}

func initTestNamingVars(t *testing.T) {
	t.Helper()
	vars := namingvars.Vars{"test": {"marker": "ok"}}
	fingerprint, err := namingvars.Fingerprint(vars)
	if err != nil {
		t.Fatalf("naming vars fingerprint: %v", err)
	}
	if err := namingvars.SetActive(vars, fingerprint); err != nil {
		t.Fatalf("activate naming vars: %v", err)
	}
}

func sizePointer(value int64) *int64 { return &value }

// The acceptance regression for this slice: one non-default selected
// parser/profile must drive target and naming planning, and no other parser may
// run. The release title carries [E07] and the torrent entry carries [E99]: the
// bundled profile would render 07, a file-name parse would render 99, and only
// the selected parser's frozen Release identity (42) may appear.
func TestTargetPlanningUsesTheSelectedParserProfile(t *testing.T) {
	initTestNamingVars(t)
	const kind = "test-target-selected-parser"
	adapter := registerRecordingParser(t, kind, "42")

	service := &Service{
		targetAcquisitionConfigured: true,
		targetProbe:                 missingTargetProbe{},
	}
	pattern := `\[E(?P<episode>\d{1,4})\]`
	snapshot := targetPlanningSnapshot(t, kind, pattern, 0)
	service.db = openRSSDatabase(t)
	seedRSSOwner(t, service.db, false, true)
	snapshot.managedAnimeID = "anime-rss-test"
	item := acquisitiondomain.FeedItem{
		GUID:  "guid-selected-parser",
		Title: "[RealGroup] Preview Show [E07] [1080p]",
		ReleaseFiles: []acquisitiondomain.ReleaseFile{
			{
				SourcePath: "Preview Show [E99].mkv",
				Filename:   "Preview Show [E99].mkv",
				SizeBytes:  sizePointer(1024),
			},
		},
	}
	release := animedomain.PreviewReleaseResult{
		StableItemKey: "guid:guid-selected-parser",
		Verdict:       animedomain.ReleaseVerdict("accepted_pending_download"),
	}

	acquisition := service.preflightTargetAcquisition(context.Background(), snapshot, item, release, nil)
	if acquisition == nil {
		t.Fatal("preflight returned no acquisition")
	}
	if acquisition.admission.Admission != target.AdmissionDownload {
		t.Fatalf("admission = %+v, want download-required", acquisition.admission)
	}
	if len(acquisition.set.Targets) != 1 {
		t.Fatalf("target set = %+v, want exactly one target", acquisition.set)
	}
	rendered := acquisition.set.Targets[0].TargetPath
	if !strings.Contains(rendered, "E42") {
		t.Fatalf("target %q did not use the selected parser's frozen episode 42", rendered)
	}
	if strings.Contains(rendered, "E07") {
		t.Fatalf("target %q re-parsed the release title with the bundled profile", rendered)
	}
	if strings.Contains(rendered, "E99") {
		t.Fatalf("target %q re-parsed the torrent file name instead of the frozen evidence", rendered)
	}

	// The same selected parser processes release and each file. Its constant 42
	// overrides both bundled-title 07 and bundled-filename 99, as asserted above.
	inputs := adapter.parsedInputs()
	if len(inputs) != 2 || inputs[0] != item.Title || inputs[1] != "Preview Show [E99].mkv" {
		t.Fatalf("selected parser inputs = %q, want release title then exact filename", inputs)
	}

	// The frozen target carries the parser provenance of that same selection.
	provenance := decodeTargetParserProvenance(t, acquisition.set.Targets[0].ConfigSnapshot)
	expected, err := parser.NewSelectionFromProfile(parser.ParserKind(kind), []string{pattern}, 0)
	if err != nil {
		t.Fatalf("rebuild expected selection: %v", err)
	}
	if provenance.Kind != kind {
		t.Fatalf("frozen target parser kind = %q, want %q", provenance.Kind, kind)
	}
	if provenance.ConfigFingerprint != expected.ConfigFingerprint {
		t.Fatalf("frozen target config fingerprint = %q, want %q", provenance.ConfigFingerprint, expected.ConfigFingerprint)
	}
	if provenance.ImplementationVersion != "test-selected-parser-v1" {
		t.Fatalf("frozen target implementation version = %q", provenance.ImplementationVersion)
	}
	if provenance.ResultDigest == "" || provenance.InputDigest == "" {
		t.Fatalf("frozen target provenance is missing the parse digests: %+v", provenance)
	}
}

func decodeTargetParserProvenance(t *testing.T, raw json.RawMessage) parser.Provenance {
	t.Helper()
	var snapshot struct {
		Parser *parser.Provenance `json:"parser"`
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatalf("decode target config snapshot %s: %v", raw, err)
	}
	if snapshot.Parser == nil {
		t.Fatalf("target config snapshot %s carries no parser provenance", raw)
	}
	return *snapshot.Parser
}

// An unknown/offline selected kind fails the admission closed. It must never
// fall back to the bundled parser: a silent fallback would be a second parser.
func TestTargetPlanningFailsClosedForUnregisteredSelectedKind(t *testing.T) {
	const kind = "test-target-unregistered-parser"
	if parser.IsRegistered(parser.ParserKind(kind)) {
		t.Fatalf("test parser kind %q must stay unregistered", kind)
	}
	service := &Service{
		targetAcquisitionConfigured: true,
		targetProbe:                 missingTargetProbe{},
	}
	snapshot := targetPlanningSnapshot(t, kind, `\[E(?P<episode>\d{1,4})\]`, 0)
	service.db = openRSSDatabase(t)
	seedRSSOwner(t, service.db, false, true)
	snapshot.managedAnimeID = "anime-rss-test"
	item := acquisitiondomain.FeedItem{
		GUID:  "guid-unregistered-parser",
		Title: "[RealGroup] Preview Show [E07] [1080p]",
		ReleaseFiles: []acquisitiondomain.ReleaseFile{
			{SourcePath: "Preview Show [E07].mkv", Filename: "Preview Show [E07].mkv", SizeBytes: sizePointer(1024)},
		},
	}
	release := animedomain.PreviewReleaseResult{
		StableItemKey: "guid:guid-unregistered-parser",
		Verdict:       animedomain.ReleaseVerdict("accepted_pending_download"),
	}
	acquisition := service.preflightTargetAcquisition(context.Background(), snapshot, item, release, nil)
	if acquisition == nil || acquisition.admission.Admission != target.AdmissionFailed {
		t.Fatalf("acquisition = %+v, want a failed admission", acquisition)
	}
	if acquisition.admission.Reason != "parser_selection_unavailable" {
		t.Fatalf("failure reason = %q, want parser_selection_unavailable", acquisition.admission.Reason)
	}
}

// The link's episode offset is applied exactly once, by the episode identity
// resolver, with explicit-zero semantics intact. The adapter never sees an
// offset.
func TestReleaseParserEvidenceAppliesOffsetExactlyOnce(t *testing.T) {
	const kind = "test-offset-selected-parser"
	adapter := registerRecordingParser(t, kind, "7")
	tests := []struct {
		name        string
		linkOffset  int
		wantEpisode string
	}{
		{name: "link offset is added", linkOffset: 3, wantEpisode: "10"},
		{name: "negative link offset is subtracted", linkOffset: -2, wantEpisode: "5"},
		{name: "zero link offset leaves the episode", linkOffset: 0, wantEpisode: "7"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := targetPlanningSnapshot(t, kind, `\[E(?P<episode>\d{1,4})\]`, test.linkOffset)
			evidence, err := resolveReleaseParserEvidence(context.Background(), snapshot, "[RealGroup] Preview Show [E07] [1080p]")
			if err != nil {
				t.Fatalf("resolve release parser evidence: %v", err)
			}
			if evidence.Parsed.Episode == nil || string(*evidence.Parsed.Episode) != "7" {
				t.Fatalf("adapter episode = %v, want the pre-offset 7 (parsers never apply offset)", evidence.Parsed.Episode)
			}
			if evidence.Identity.RawEpisode == nil || string(*evidence.Identity.RawEpisode) != "7" {
				t.Fatalf("frozen raw episode = %v, want 7", evidence.Identity.RawEpisode)
			}
			if evidence.Identity.Episode == nil || string(*evidence.Identity.Episode) != test.wantEpisode {
				t.Fatalf("frozen episode = %v, want %s (offset applied exactly once)", evidence.Identity.Episode, test.wantEpisode)
			}
			if evidence.Identity.Offset != test.linkOffset {
				t.Fatalf("frozen offset = %d, want %d", evidence.Identity.Offset, test.linkOffset)
			}
			namingFacts := evidence.namingFacts()
			if namingFacts == nil || namingFacts.Number != test.wantEpisode || namingFacts.Offset != test.linkOffset {
				t.Fatalf("frozen naming facts = %+v, want episode %s offset %d", namingFacts, test.wantEpisode, test.linkOffset)
			}
		})
	}
	if inputs := adapter.parsedInputs(); len(inputs) != len(tests) {
		t.Fatalf("selected parser ran %d times, want %d (one per evaluation)", len(inputs), len(tests))
	}
}

// The frozen DownloadTask policy names the same selection as the target and
// Release evidence, so a retry never reads current instance parser policy.
func TestFrozenReleasePolicyCarriesTheSelectedParserProvenance(t *testing.T) {
	const kind = "test-policy-selected-parser"
	registerRecordingParser(t, kind, "42")
	pattern := `\[E(?P<episode>\d{1,4})\]`
	snapshot := targetPlanningSnapshot(t, kind, pattern, 0)
	evidence, err := resolveReleaseParserEvidence(context.Background(), snapshot, "[RealGroup] Preview Show [E07] [1080p]")
	if err != nil {
		t.Fatalf("resolve release parser evidence: %v", err)
	}
	raw, err := encodeFrozenReleasePolicy(snapshot, evidence)
	if err != nil {
		t.Fatalf("encode frozen release policy: %v", err)
	}
	var policy frozenPolicySnapshot
	if err := json.Unmarshal(raw, &policy); err != nil {
		t.Fatalf("decode frozen release policy %s: %v", raw, err)
	}
	if policy.Parser == nil {
		t.Fatalf("frozen release policy %s carries no parser provenance", raw)
	}
	if policy.ConfigIdentity.IdentityKind != "instance_materialized" || policy.ConfigIdentity.ManagedAnimeID == nil || *policy.ConfigIdentity.ManagedAnimeID != snapshot.managedAnimeID || policy.ConfigIdentity.CandidateID != nil || policy.ConfigIdentity.Revision != nil {
		t.Fatalf("ordinary admission fabricated the wrong config identity: %+v", policy.ConfigIdentity)
	}
	if policy.EffectiveDigest == "" || policy.EffectiveDigest != policy.ConfigIdentity.EffectiveDigest || len(policy.EffectiveSections) == 0 || len(policy.SectionVersions) != len(animedomain.EffectiveSectionOrder) || len(policy.SectionSchemas) != len(animedomain.EffectiveSectionOrder) {
		t.Fatalf("frozen release policy is incomplete: %+v", policy)
	}
	expected, err := parser.NewSelectionFromProfile(parser.ParserKind(kind), []string{pattern}, 0)
	if err != nil {
		t.Fatalf("rebuild expected selection: %v", err)
	}
	if policy.Parser.Kind != kind || policy.Parser.ConfigFingerprint != expected.ConfigFingerprint {
		t.Fatalf("frozen policy parser = %+v, want kind %q fingerprint %q", policy.Parser, kind, expected.ConfigFingerprint)
	}
	if policy.Parser.ParseStatus != string(parser.StatusValue) || policy.Parser.ParseShape != string(parser.ShapeSingle) {
		t.Fatalf("frozen policy lost the typed parse result: status=%q shape=%q", policy.Parser.ParseStatus, policy.Parser.ParseShape)
	}
}

// The frozen provenance carries the link's effective offset and the selected
// parser's typed result, all from the same selection, with the source CAS
// version the offset was read from.
func TestReleaseParserEvidenceProvenanceCarriesOffsetAndTypedShape(t *testing.T) {
	const kind = "test-provenance-selected-parser"
	registerRecordingParser(t, kind, "7")
	snapshot := targetPlanningSnapshot(t, kind, `\[E(?P<episode>\d{1,4})\]`, 5)
	snapshot.sourceVersion = 21
	evidence, err := resolveReleaseParserEvidence(context.Background(), snapshot, "[RealGroup] Preview Show [E07] [1080p]")
	if err != nil {
		t.Fatalf("resolve release parser evidence: %v", err)
	}
	provenance := evidence.provenance()
	if provenance.SourceVersion != 21 {
		t.Fatalf("frozen source version = %d, want 21", provenance.SourceVersion)
	}
	if provenance.Offset != 5 {
		t.Fatalf("frozen effective offset = %d, want 5", provenance.Offset)
	}
	if provenance.ParseStatus != string(parser.StatusValue) || provenance.ParseShape != string(parser.ShapeSingle) {
		t.Fatalf("frozen typed parse = %q/%q, want value/single", provenance.ParseStatus, provenance.ParseShape)
	}
	if provenance.Kind != kind || provenance.PolicyVersion != 11 || provenance.ProfileVersion != 7 {
		t.Fatalf("frozen provenance lost the owner versions: %+v", provenance)
	}
}

// recordingMediaRepository captures the frozen Release media fact snapshot
// without a database, so the Release evidence row can be asserted directly.
type recordingMediaRepository struct {
	snapshots []facts.Snapshot
}

func (r *recordingMediaRepository) CreateReleaseSnapshotTx(_ context.Context, _ txscope.Tx, snapshot facts.Snapshot) error {
	r.snapshots = append(r.snapshots, snapshot)
	return nil
}

func (r *recordingMediaRepository) UpsertFileFactsTx(context.Context, txscope.Tx, facts.FileRow) error {
	return nil
}

func (r *recordingMediaRepository) ListFileRows(context.Context, string) ([]facts.FileRow, error) {
	return nil, nil
}

func (r *recordingMediaRepository) ListFileRowsTx(context.Context, txscope.Tx, string) ([]facts.FileRow, error) {
	return nil, nil
}

func (r *recordingMediaRepository) ListFileRowsBySnapshots(context.Context, []string) (map[string][]facts.FileRow, error) {
	return nil, nil
}

// The frozen Release evidence must carry the same non-default selection: kind,
// config fingerprint, exact config, and the identity that selection resolved.
func TestReleaseMediaSnapshotFreezesTheSelectedParserProfile(t *testing.T) {
	const kind = "test-release-evidence-parser"
	registerRecordingParser(t, kind, "42")
	pattern := `\[E(?P<episode>\d{1,4})\]`
	title := "[RealGroup] Preview Show [E07] [1080p]"
	snapshot := targetPlanningSnapshot(t, kind, pattern, 0)
	evidence, err := resolveReleaseParserEvidence(context.Background(), snapshot, title)
	if err != nil {
		t.Fatalf("resolve release parser evidence: %v", err)
	}
	repository := &recordingMediaRepository{}
	plan := &batchplan.Plan{Version: batchplan.Version, UnclassifiedRoute: batchplan.RouteSkip, CoverageKeys: []string{"tv:s1:e42"}}
	if err := persistReleaseMediaSnapshot(context.Background(), nil, repository, snapshot, title, evidence, plan); err != nil {
		t.Fatalf("persist release media snapshot: %v", err)
	}
	if len(repository.snapshots) != 1 {
		t.Fatalf("snapshots written = %d, want 1", len(repository.snapshots))
	}
	row := repository.snapshots[0]
	expected, err := parser.NewSelectionFromProfile(parser.ParserKind(kind), []string{pattern}, 0)
	if err != nil {
		t.Fatalf("rebuild expected selection: %v", err)
	}
	if row.ParserKind != kind {
		t.Fatalf("frozen parser kind = %q, want %q", row.ParserKind, kind)
	}
	if row.ParserConfigFingerprint != expected.ConfigFingerprint {
		t.Fatalf("frozen config fingerprint = %q, want %q", row.ParserConfigFingerprint, expected.ConfigFingerprint)
	}
	if string(row.ParserConfig) != string(expected.Parser.Config) {
		t.Fatalf("frozen config = %s, want %s", row.ParserConfig, expected.Parser.Config)
	}
	if row.ReleaseIdentity.State != "resolved" || row.ReleaseIdentity.Episode == nil || string(*row.ReleaseIdentity.Episode) != "42" {
		t.Fatalf("frozen release identity = %+v, want the selected parser's resolved 42", row.ReleaseIdentity)
	}
	if row.ReleaseFacts.EpisodeEvidence == nil || row.ReleaseFacts.EpisodeEvidence.Episode == nil ||
		string(*row.ReleaseFacts.EpisodeEvidence.Episode) != "42" {
		t.Fatalf("frozen release episode evidence = %+v, want 42", row.ReleaseFacts.EpisodeEvidence)
	}
	if row.ReleaseFacts.EpisodeEvidence.InputKind != string(parser.InputReleaseTitle) {
		t.Fatalf("frozen release evidence input kind = %q", row.ReleaseFacts.EpisodeEvidence.InputKind)
	}
	if row.ReleaseFacts.BatchPlan == nil || row.ReleaseFacts.BatchPlan.UnclassifiedRoute != batchplan.RouteSkip {
		t.Fatalf("frozen batch plan = %+v, want the admission plan with its route", row.ReleaseFacts.BatchPlan)
	}
}

// The media-fact input digest is the re-evaluation driver: an instance parser
// link offset, parser kind, or profile change must produce a
// different digest (fresh snapshot) while an unchanged policy keeps it stable.
func TestMediaFactsInputDigestTracksSelectedParserInputs(t *testing.T) {
	const kind = "test-digest-selected-parser"
	registerRecordingParser(t, kind, "7")
	pattern := `\[E(?P<episode>\d{1,4})\]`

	base := targetPlanningSnapshot(t, kind, pattern, 0)
	baseEvidence, err := resolveReleaseParserEvidence(context.Background(), base, "[RealGroup] Show [E07]")
	if err != nil {
		t.Fatalf("resolve base evidence: %v", err)
	}
	baseDigest := target.ReleaseMediaInputDigest(releaseMediaSnapshotInput(base, "[RealGroup] Show [E07]", baseEvidence, nil))

	repeat := targetPlanningSnapshot(t, kind, pattern, 0)
	repeatEvidence, err := resolveReleaseParserEvidence(context.Background(), repeat, "[RealGroup] Show [E07]")
	if err != nil {
		t.Fatalf("resolve repeat evidence: %v", err)
	}
	if repeatDigest := target.ReleaseMediaInputDigest(releaseMediaSnapshotInput(repeat, "[RealGroup] Show [E07]", repeatEvidence, nil)); repeatDigest != baseDigest {
		t.Fatal("an unchanged selection must keep the same media facts input digest")
	}

	offset := targetPlanningSnapshot(t, kind, pattern, 3)
	offsetEvidence, err := resolveReleaseParserEvidence(context.Background(), offset, "[RealGroup] Show [E07]")
	if err != nil {
		t.Fatalf("resolve offset evidence: %v", err)
	}
	if digest := target.ReleaseMediaInputDigest(releaseMediaSnapshotInput(offset, "[RealGroup] Show [E07]", offsetEvidence, nil)); digest == baseDigest {
		t.Fatal("a link offset change must change the digest")
	}

	profile := targetPlanningSnapshot(t, kind, `S(?P<episode>\d{1,4})`, 0)
	profileEvidence, err := resolveReleaseParserEvidence(context.Background(), profile, "[RealGroup] Show [E07]")
	if err != nil {
		t.Fatalf("resolve profile evidence: %v", err)
	}
	if digest := target.ReleaseMediaInputDigest(releaseMediaSnapshotInput(profile, "[RealGroup] Show [E07]", profileEvidence, nil)); digest == baseDigest {
		t.Fatal("a parser profile change must change the digest")
	}
}
