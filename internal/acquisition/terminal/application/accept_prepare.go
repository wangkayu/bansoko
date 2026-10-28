package application

import (
	generated "bansoko/internal/acquisition/terminal/adapter/persistence/generated"
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	acq "bansoko/internal/acquisition/domain"
	target "bansoko/internal/acquisition/target"
	protocol "bansoko/internal/acquisition/terminal/domain"
	anime "bansoko/internal/anime/domain"
	catalog "bansoko/internal/catalog/domain"
	"bansoko/internal/filekinds"
	"bansoko/internal/namingvars"
	mediaparser "bansoko/internal/parser"
	"bansoko/internal/postprocessing/naming"
	"bansoko/internal/templatefacts"
)

type admissionEvaluation struct {
	inputDigest    string
	input          protocol.Value
	manifest       catalog.TorrentManifest
	payload        []byte
	witness        preparationWitness
	varsCompletion varsEvaluationCompletion
}

func valueOf(v any) (protocol.Value, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return protocol.Value{}, err
	}
	return protocol.ParseStrict(raw)
}
func frozenValue(state string, value any) map[string]any {
	return map[string]any{"state": state, "value": value, "reason_code": nil}
}

func evaluateAdmission(ctx context.Context, c admissionCapture, intent acquisitionIntent, supplied ...target.ParserEvidence) (admissionEvaluation, error) {
	seed, err := admissionSeed(c)
	if err != nil {
		return admissionEvaluation{}, err
	}
	if seed == nil || seed.Manifest == nil {
		return admissionEvaluation{}, materialError("inventory", nil)
	}
	b := c.material.config.business
	effective, err := anime.EffectiveConfigFromSections(captureSections(c.material))
	if err != nil {
		return admissionEvaluation{}, err
	}
	// The one offset is the release's RSS link offset for every config kind; a
	// one-shot placeholder link carries the one-shot input's own offset.
	selection := target.ParserSelectionInput{Kind: b.Parser.Kind, EpisodePatterns: effective.Rules.Parser.EpisodePatterns, Offset: c.material.release.offset, SourceVersion: c.material.release.sourceVersion, PolicyVersion: b.Parser.PolicyVersion, ProfileVersion: b.Parser.ProfileVersion, RulesOwnerVersion: b.Parser.RulesOwnerVersion}
	if c.configKind == "owner" {
		selection.Kind, selection.PolicyVersion = c.material.parserKind, c.material.parserVersion
		selection.RulesOwnerVersion = c.material.rulesOwnerVersion
	}
	selection.MediaKind, selection.Season, selection.SeasonBound = anime.FrozenMediaBinding(c.material.tmdb, c.material.mediaKindOverride)
	var evidence target.ParserEvidence
	if !mediaparser.IsRegistered(mediaparser.ParserKind(selection.Kind)) {
		return admissionEvaluation{}, acceptError(422, "INPUT_SCHEMA_UNSUPPORTED", "parser_unavailable", nil)
	}
	if len(supplied) > 0 {
		evidence = supplied[0]
		p := evidence.Provenance()
		if p.Kind != selection.Kind || p.PolicyVersion != selection.PolicyVersion || p.Offset != selection.Offset {
			return admissionEvaluation{}, acceptError(409, "INPUT_STALE", "parser_policy_changed", nil)
		}
	} else {
		evidence, err = target.ResolveParserEvidence(ctx, selection, seed.Title)
	}
	if err != nil {
		return admissionEvaluation{}, acceptError(422, "INPUT_SCHEMA_UNSUPPORTED", "parser_unavailable", err)
	}
	item := acq.FeedItem{Title: seed.Title}
	hold := admissionMetadataHold(c)
	release := anime.EvaluateFeedItemWithIdentity(item, anime.SourceEvaluationInput{}, effective, hold, c.material.programs.attr, evidence.Identity)
	if string(release.Verdict) == "waiting_metadata" {
		if !bypassMetadataHold(intent.entryMode) {
			return admissionEvaluation{}, acceptError(422, "ADMISSION_REJECTED", "metadata_hold", nil)
		}
	} else if string(release.Verdict) != "accepted_pending_download" && !((intent.entryMode == "one_shot_seed" || intent.entryMode == "one_shot_snapshot") && string(release.Verdict) == "parse_failed") {
		return admissionEvaluation{}, acceptError(422, "ADMISSION_REJECTED", "release_rejected", nil)
	}
	required := []string{}
	// A required file is a first-level video the file rules select (under a
	// one-shot manual selection: a selected video at any depth): nested or
	// unselected files, other kinds and rule-excluded videos are never
	// required, and subtitles only follow a video.
	for _, file := range seed.Manifest.Files {
		if filekinds.Classify(filekinds.Version, file.SourcePath) != filekinds.KindVideo {
			continue
		}
		if anime.ManifestFileExclusionIn(filekinds.Version, effective.Rules.FileFilter, file.SourcePath, intent.fileSelection) == "" {
			required = append(required, file.FileRef)
		}
	}
	if len(required) == 0 {
		return admissionEvaluation{}, acceptError(422, "NO_REQUIRED_FILES", "no_required_files", nil)
	}
	sort.Strings(required)
	// No simplified input is persisted. Every execution group is validated by
	// the existing closed registry; external transport cannot pass this value.
	var completion varsEvaluationCompletion
	config, err := evaluationConfig(c, evidence, ctx, &completion)
	if err != nil {
		return admissionEvaluation{}, err
	}
	// anime.group is the source group's display name, the fact the release's own
	// rss_sources row records and every naming render reads back; the provider
	// group identity is display-blind and never becomes a name. The column is
	// NOT NULL with a non-blank CHECK, so the name is always present: there is no
	// "source without a recorded name" for this entry to fall back for.
	source := map[string]any{"kind": intent.entryMode, "release": frozenValue("known", map[string]any{"ref": c.material.release.id, "version": c.material.release.version}), "stable_item_key": frozenValue("known", seed.StableSeedKey), "title": frozenValue("known", seed.Title), "published_at": frozenValue("absent", nil), "provider": frozenValue("absent", nil), "group": frozenValue("known", strings.TrimSpace(c.material.release.groupName)), "payload_digest": c.payloadDigest, "manifest_digest": seed.Manifest.ProjectionDigest, "infohash_v1": frozenValue("absent", nil), "infohash_v2": frozenValue("absent", nil), "locator_display": frozenValue("known", seed.LocatorDisplay), "locator_digest": frozenValue("known", catalog.DigestBytes([]byte(seed.Locator))), "download_provider": frozenValue("absent", nil)}
	if c.material.catalog != nil {
		source["provider"] = frozenValue("known", c.material.catalog.feed.Provider)
	}
	if seed.Manifest.InfoHashV1 != "" {
		source["infohash_v1"] = frozenValue("known", seed.Manifest.InfoHashV1)
	}
	if seed.Manifest.InfoHashV2 != "" {
		source["infohash_v2"] = frozenValue("known", seed.Manifest.InfoHashV2)
	}
	// The torrent totals are frozen from the same manifest the files come
	// from, so every notification trigger reads one value (torrent.size /
	// torrent.file_count). The accept function re-derives both from the
	// manifest rows and refuses a mismatch.
	var torrentSize int64
	for _, file := range seed.Manifest.Files {
		torrentSize += file.SizeBytes
	}
	source["torrent_size"] = frozenValue("known", torrentSize)
	source["torrent_file_count"] = frozenValue("known", len(seed.Manifest.Files))
	ruleDigest := protocol.Digest("acquisition-file-rules:v1", captureSections(c.material).Sections[anime.SectionFileRules])
	delivery, err := protocol.ParseStrict(protocol.Canonical(intent.delivery))
	if err != nil {
		return admissionEvaluation{}, err
	}
	delivery.Object["naming"].Object["vars_choice"] = protocol.Value{Kind: protocol.KindString, String: "frozen"}
	media, err := protocol.ParseStrict(protocol.Canonical(intent.media))
	if err != nil {
		return admissionEvaluation{}, err
	}
	if c.material.bgm != nil && media.Object["bgm"].Object["value"].Object["ref"].String != c.material.bgm.WorkId {
		return admissionEvaluation{}, materialError("media_binding", nil)
	}
	if c.material.tmdb != nil {
		v := media.Object["tmdb"].Object["value"]
		if v.Object["work_ref"].String != c.material.tmdb.WorkId || v.Object["kind"].String != string(c.material.tmdb.MediaKind) {
			return admissionEvaluation{}, materialError("media_binding", nil)
		}
	}
	if c.material.bgm == nil || strings.TrimSpace(c.material.bgm.WorkId) == "" {
		media.Object["bgm"], err = valueOf(frozenValue("absent", nil))
		if err != nil {
			return admissionEvaluation{}, err
		}
	}
	if c.material.tmdb == nil {
		media.Object["tmdb"], err = valueOf(frozenValue("absent", nil))
		if err != nil {
			return admissionEvaluation{}, err
		}
	}
	// Schema 5 freezes the bound works' recorded native titles, read in
	// this transaction (captureWorkTitlesTx); the accept function re-derives
	// both from the same rows and refuses a mismatch.
	bgmTitles, tmdbTitles := any(frozenValue("absent", nil)), any(frozenValue("absent", nil))
	if t := c.material.bgmTitles; t != nil && c.material.bgm != nil && strings.TrimSpace(c.material.bgm.WorkId) != "" {
		bgmTitles = frozenWorkTitles(t, map[string]string{"title": t.title, "title_cn": t.titleCN})
	}
	if t := c.material.tmdbTitles; t != nil && c.material.tmdb != nil {
		tmdbTitles = frozenWorkTitles(t, map[string]string{"title_cn": t.titleCN, "original_title": t.originalTitle, "overview": t.overview})
	}
	if media.Object["bgm_titles"], err = valueOf(bgmTitles); err != nil {
		return admissionEvaluation{}, err
	}
	if media.Object["tmdb_titles"], err = valueOf(tmdbTitles); err != nil {
		return admissionEvaluation{}, err
	}
	version := 1
	schema := "AcquisitionInputV1"
	input, err := valueOf(map[string]any{"schema_version": version, "source": source, "selection": map[string]any{"mode": "rules", "rules_digest": ruleDigest, "explicit_refs": []string{}}, "config": json.RawMessage(protocol.Canonical(config)), "media": json.RawMessage(protocol.Canonical(media)), "delivery": json.RawMessage(protocol.Canonical(delivery)), "limits": json.RawMessage(protocol.Canonical(intent.limits)), "placement": map[string]any{"mode": "none"}})
	if err == nil {
		err = protocol.Validate(schema, input)
	}
	if err != nil {
		return admissionEvaluation{}, err
	}
	raw := protocol.Canonical(input)
	digest := protocol.Digest("acquisition-input:v1", raw)
	return admissionEvaluation{inputDigest: digest, input: input, manifest: *seed.Manifest, payload: append([]byte(nil), seed.Payload...), varsCompletion: completion}, nil
}
func evaluationConfig(c admissionCapture, e target.ParserEvidence, ctx context.Context, completion *varsEvaluationCompletion) (protocol.Value, error) {
	resolved := captureSections(c.material)
	keys := append([]string(nil), anime.EffectiveSectionOrder...)
	sort.Strings(keys)
	sections := []any{}
	for _, key := range keys {
		value, err := protocol.ParseStrict(resolved.Sections[key])
		if err != nil {
			return protocol.Value{}, err
		}
		if key == anime.SectionSubgroups {
			value, err = protocol.ParseStrict([]byte(c.policyCanonical))
			if err != nil {
				return protocol.Value{}, err
			}
		}
		if key == anime.SectionNamingRules {
			value, err = projectNamingSection(resolved.Sections[key])
			if err != nil {
				return protocol.Value{}, err
			}
		}
		sections = append(sections, map[string]any{"section_key": key, "schema_version": resolved.SchemaVersions[key], "config_version": resolved.Versions[key], "value": json.RawMessage(protocol.Canonical(value))})
	}
	p := e.Provenance()
	var config any
	if len(p.Config) > 0 {
		config = json.RawMessage(p.Config)
	}
	selection := map[string]any{"kind": p.Kind, "config": config, "config_fingerprint": p.ConfigFingerprint, "config_version": p.ConfigVersion, "implementation_version": p.ImplementationVersion, "output_contract_version": p.OutputContractVersion, "normalizer_version": p.NormalizerVersion, "resolver_version": p.ResolverVersion, "offset": p.Offset}
	identity := map[string]any{"kind": "one_shot", "intent_digest": c.requestHash}
	if c.configKind == "saved_revision" {
		identity = map[string]any{"kind": "saved_revision", "candidate_ref": c.material.config.candidateID, "revision": c.material.config.revision, "managed_anime_ref": frozenValue("known", c.material.management.managedID)}
	}
	vars, err := evaluateFrozenVars(c, e, ctx, completion)
	if err != nil {
		return protocol.Value{}, err
	}
	var rules any = frozenValue("absent", nil)
	if c.configKind == "owner" {
		if c.material.rulesOwnerVersion < 1 || p.RulesOwnerVersion != c.material.rulesOwnerVersion {
			return protocol.Value{}, acceptError(500, "INTERNAL_INVARIANT", "rules_owner_provenance_mismatch", nil)
		}
		rules = frozenValue("known", c.material.rulesOwnerVersion)
	} else {
		// Native provenance describes this acceptance branch, not a historical
		// owner recorded in the frozen business envelope.
		p.RulesOwnerVersion = 0
	}
	provenance := nativeParserProvenance(p, selection)
	return valueOf(map[string]any{"identity": identity, "effective_digest": c.configDigest, "sections": sections, "parser": map[string]any{"selection": frozenValue("known", selection), "provenance": frozenValue("known", provenance), "runtime_version": "parser-v1"}, "vars": vars, "rules_version": rules})
}
func prepareAdmission(c admissionCapture, e admissionEvaluation, intent acquisitionIntent, a acceptAccess, r requestIdentity, limits witnessLimits) (preparedNative, error) {
	qualification, err := protocol.ParseStrict(c.qualification.Canonical())
	if err != nil {
		return preparedNative{}, err
	}
	management := qualification.Object["management"]
	if management.Object["kind"].String == "instance" {
		policy := management.Object["subscription_policy"].String
		if !(intent.entryMode == "automatic_source" && policy == "automatic_active" || intent.entryMode == "subscription_direct" && policy == "manual_active_or_stopped") {
			return preparedNative{}, acceptError(500, "INTERNAL_INVARIANT", "entry_subscription_policy_mismatch", nil)
		}
	}
	if e.inputDigest != protocol.Digest("acquisition-input:v1", protocol.Canonical(e.input)) || e.input.Object["config"].Object["effective_digest"].String != c.configDigest {
		return preparedNative{}, errWitnessMismatch
	}
	witness := preparationWitness{}
	if c.material.catalog != nil {
		var err error
		witness, err = constructWitness(c, e.inputDigest, limits)
		if err != nil {
			return preparedNative{}, err
		}
		q, err := protocol.ParseStrict(c.qualification.Canonical())
		if err != nil {
			return preparedNative{}, err
		}
		selected := false
		var body map[string]any
		if err := json.Unmarshal([]byte(witness.canonical), &body); err != nil {
			return preparedNative{}, err
		}
		for _, ref := range body["selected_refs"].([]any) {
			if ref == q.Object["feed"].Object["group_ref"].String {
				selected = true
			}
		}
		if !selected {
			return preparedNative{}, acceptError(422, "ADMISSION_REJECTED", "group_not_selected", nil)
		}
	}
	id, task, submit, settle := newAcceptIDs()
	retention := intent.limits.Object["retention"].Object
	files := []any{}
	for _, f := range e.manifest.Files {
		files = append(files, map[string]any{"file_ref": f.FileRef, "ordinal": f.Ordinal, "relative_path": f.SourcePath, "exact_size": f.SizeBytes, "media_kind": f.MediaKind, "identity_evidence": map[string]any{}})
	}
	filesValue, err := valueOf(files)
	if err != nil {
		return preparedNative{}, err
	}
	configIdentity, err := protocol.ParseStrict(protocol.Canonical(e.input.Object["config"].Object["identity"]))
	if err != nil {
		return preparedNative{}, err
	}
	if configIdentity.Object["kind"].String == "saved_revision" {
		configIdentity.Object["revision"] = protocol.Value{Kind: protocol.KindString, String: decimalRevision(c.material.config.revision)}
	}
	raw := string(protocol.Canonical(e.input))
	args := generatedParams(id, task, submit, settle)
	args.EntryKind = sqlString("one_shot")
	args.AccessScope = sqlString(a.scope)
	args.RequestNamespace = sqlString(r.namespace)
	args.RequestKey = sqlString(r.key)
	args.RequestHash = sqlString(r.hash)
	args.CanonicalRequest = sqlJSON(r.canonical)
	args.AdmissionDigest = sqlString(e.inputDigest)
	args.ConfigIdentity = sqlJSON(string(protocol.Canonical(configIdentity)))
	title, _ := valueOf(frozenValue("known", intent.title))
	args.DisplayTitle = sqlJSON(string(protocol.Canonical(title)))
	args.OriginManagedAnimeRef = sqlString(c.material.management.managedID)
	args.DetailRetentionSeconds = sqlInt(retention["detail_seconds"].Int)
	args.ReceiptAfterAcceptSeconds = sqlInt(retention["receipt_after_accept_seconds"].Int)
	args.ReceiptAfterTerminalSeconds = sqlInt(retention["receipt_after_terminal_seconds"].Int)
	if c.material.catalog != nil {
		args.PreparationMode = sqlString("provider_selection_v1")
		args.PreparationDigest = sqlString(witness.digest)
		args.PreparationWitness = sqlString(witness.canonical)
	} else {
		args.PreparationMode = sqlString("none")
	}
	args.InputCanonicalUtf8 = sqlString(raw)
	args.Payload = e.payload
	args.ValidatedInfohashV1 = sqlString(e.manifest.InfoHashV1)
	if e.manifest.InfoHashV1 == "" {
		args.ValidatedInfohashV1.Valid = false
	}
	if e.manifest.InfoHashV2 != "" {
		args.ValidatedInfohashV2 = sqlString(e.manifest.InfoHashV2)
	}
	args.ManifestDigest = sqlString(e.manifest.ProjectionDigest)
	args.ManifestFiles = sqlJSON(string(protocol.Canonical(filesValue)))
	args.SourceReleaseID = sqlString(c.material.release.id)
	args.CorrelationID = sqlString("corr-" + id)
	args.CausationType = sqlString("release")
	args.CausationID = sqlString(c.material.release.id)
	if intent.submit != nil {
		p, snapshot, err := projectSubmit(*intent.submit)
		if err != nil {
			return preparedNative{}, err
		}
		args.TaskFrozenInput = sqlJSON(string(p.frozenInput))
		args.TaskFrozenPolicy = sqlJSON(string(p.frozenPolicy))
		args.TaskIdempotencyKey = sqlString(p.effectKey)
		args.SubmitInputSnapshot = sqlJSON(string(snapshot))
		args.CorrelationID = sqlString(p.correlation)
		args.CausationType = sqlString(p.causationType)
		args.CausationID = sqlString(p.causationID)
	}
	if intent.submit == nil {
		args.TaskFrozenInput = sqlJSON(raw)
		args.TaskFrozenPolicy = sqlJSON(string(protocol.Canonical(e.input.Object["config"])))
		args.SubmitInputSnapshot = sqlJSON(raw)
	}
	args.MaxAttempts = sqlInt(intent.limits.Object["submit"].Object["max_attempts"].Int)
	args.Qualification = sqlJSON(string(c.qualification.Canonical()))
	args.BusinessSnapshot = sqlJSON(c.businessJSON)
	args.WitnessMaxBytes = sqlInt(int64(limits.bytes))
	args.WitnessMaxRows = sqlInt(int64(limits.unionRows))
	record, err := e.varsCompletion.record(e.input.Object["config"].Object["vars"], id, e.inputDigest)
	if err != nil {
		return preparedNative{}, err
	}
	return preparedNative{args: args, varsRecord: record}, nil
}

func decimalRevision(v int64) string { return strconv.FormatInt(v, 10) }
func generatedParams(id, task, submit, settle string) generated.AcceptQualifiedQueuedParams {
	return generated.AcceptQualifiedQueuedParams{AcquisitionID: sqlString(id), DownloadTaskID: sqlString(task), TaskIdempotencyKey: sqlString(task), SubmitWorkItemID: sqlString(submit), SettleWorkItemID: sqlString(settle), SettleIdempotencyKey: sqlString(settle)}
}

func evaluateFrozenVars(c admissionCapture, e target.ParserEvidence, ctx context.Context, completion *varsEvaluationCompletion) (any, error) {
	if c.material.programs.varsSource == "" {
		return nil, materialError("programs", nil)
	}
	seed, err := admissionSeed(c)
	if err != nil {
		return nil, err
	}
	evidence := namingvars.ReleaseEvidence{
		AnimeEvidence: acceptAnimeEvidence(c.material),
		ReleaseTitle:  seed.Title, ReleaseID: c.material.release.id,
		Episode: naming.VarsEpisode(e.NamingFacts()),
		HashV1:  seed.Manifest.InfoHashV1, HashV2: seed.Manifest.InfoHashV2,
	}
	// Undecodable source facts leave rt / anibt null, as naming reads them.
	evidence.Tags, _ = templatefacts.FromSourceFacts(c.material.releaseSourceFacts)
	files := []any{}
	for _, file := range seed.Manifest.Files {
		evidence.Files = append(evidence.Files, namingvars.File{Name: file.Filename, Path: file.SourcePath, Size: file.SizeBytes})
		files = append(files, map[string]any{"name": file.Filename, "path": file.SourcePath, "size": file.SizeBytes})
	}
	input := namingvars.BuildInput(evidence)
	result, err := evaluateVarsCompletion(ctx, c.material.programs.vars, input, serverVarsEvidenceClock)
	if namingvars.IsInterrupted(err) {
		// Transient: nothing is frozen or committed; the acceptance may retry.
		return nil, acceptError(503, "ACCEPTANCE_UNKNOWN", "vars_evaluation_interrupted", err)
	}
	if err != nil {
		return nil, acceptError(422, "INPUT_SCHEMA_UNSUPPORTED", "vars_evaluation_failed", err)
	}
	*completion = result
	output := result.output
	ref := c.material.programs.vars.Ref()
	runtime := c.material.programs.vars.RuntimeVersion()
	program := map[string]any{"schema_version": 1, "source": c.material.programs.varsSource, "digest": ref.Digest, "version": ref.Version, "runtime_version": runtime, "timeout_ms": namingvars.DefaultTimeout.Milliseconds(), "max_input_bytes": 262144, "max_output_bytes": 8192, "max_collection_len": 256, "max_string_bytes": 8192}
	// known_inputs records the evidence the program read: the title, the files
	// and the release fact object (VarsInputV1).
	known := map[string]any{"release_title": frozenValue("known", seed.Title), "files": files, "facts": input.Facts}
	return map[string]any{"mode": "frozen", "program": program, "known_inputs": known, "output": output, "status": "ok", "reason_code": nil}, nil
}

// acceptAnimeEvidence is the Vars anime evidence this acceptance freezes: the
// managed anime's own titles (base, override, provider), the material's
// binding and the bound works' recorded titles (the same values media.title /
// title_override / provider_title / bgm / tmdb / *_titles freeze).
func acceptAnimeEvidence(m qualificationMaterial) namingvars.AnimeEvidence {
	evidence := namingvars.AnimeEvidence{
		AnimeID:            m.management.managedID,
		AnimeTitle:         m.management.title,
		AnimeTitleOverride: m.management.titleOverride,
		AnimeProviderTitle: m.management.providerTitle,
	}
	if m.animeYear != nil {
		year := int64(*m.animeYear)
		evidence.AnimeYear = &year
	}
	if m.animeSeason != nil {
		season := int64(*m.animeSeason)
		evidence.AnimeSeason = &season
	}
	if m.bgm != nil && strings.TrimSpace(m.bgm.WorkId) != "" {
		evidence.BgmID = m.bgm.WorkId
		if t := m.bgmTitles; t != nil {
			evidence.BgmTitle, evidence.BgmTitleCN = t.title, t.titleCN
			if t.year != nil {
				year := int64(*t.year)
				evidence.BgmYear = &year
			}
		}
	}
	if m.tmdb != nil {
		season := int64(m.tmdb.Season)
		evidence.TmdbID, evidence.TmdbKind, evidence.TmdbSeason = m.tmdb.WorkId, string(m.tmdb.MediaKind), &season
		if t := m.tmdbTitles; t != nil {
			evidence.TmdbTitleCN, evidence.TmdbOriginalTitle = t.titleCN, t.originalTitle
			if t.year != nil {
				year := int64(*t.year)
				evidence.TmdbYear = &year
			}
		}
	}
	return evidence
}
