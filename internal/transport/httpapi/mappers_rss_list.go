package httpapi

import (
	"fmt"
	"strings"

	animedomain "bansoko/internal/anime/domain"
	oneshotapplication "bansoko/internal/anime/oneshot/application"
	readmodelapplication "bansoko/internal/anime/readmodel/application"
	"bansoko/internal/apperrors"
	animecatalogdomain "bansoko/internal/catalog/domain"
	"bansoko/internal/transport/httpapi/generated"
)

// mapPreviewFeedSnapshotRefs projects the advisory provider feed snapshot
// identities of one read request. PreviewFeedSnapshotRef survives the flat
// preview retirement: it is the RSSListView advisory feed identity.
func mapPreviewFeedSnapshotRefs(refs *[]generated.PreviewFeedSnapshotRef) []readmodelapplication.PreviewFeedSnapshotRef {
	if refs == nil {
		return nil
	}
	result := make([]readmodelapplication.PreviewFeedSnapshotRef, 0, len(*refs))
	for _, ref := range *refs {
		result = append(result, readmodelapplication.PreviewFeedSnapshotRef{
			Provider:         animedomain.ProviderKind(ref.Provider),
			ProviderEntryRef: ref.ProviderEntryRef,
			ProviderGroupRef: ref.ProviderGroupRef,
			SnapshotID:       string(ref.SnapshotId),
			SnapshotVersion:  int64(ref.SnapshotVersion),
			ContentHash:      ref.ContentHash,
		})
	}
	return result
}

// mapRSSListReadInput projects the requested configuration identity. The two
// input sources are mutually exclusive; mixing them is a client error rather
// than a silent preference for one.
func mapRSSListReadInput(value generated.RSSListViewInput) (readmodelapplication.ReadRSSListViewInput, error) {
	result := readmodelapplication.ReadRSSListViewInput{InputSource: string(value.InputSource), Feed: mapPreviewFeedSnapshotRefs(value.Feed)}
	if value.LimitPerGroup != nil {
		result.LimitPerGroup = *value.LimitPerGroup
	}
	// An omitted episode_offset is the preview's 0 (no shift), the same default
	// a one-shot request uses.
	if value.EpisodeOffset != nil {
		result.PreviewEpisodeOffset = *value.EpisodeOffset
	}
	if value.OneShot != nil && *value.OneShot {
		if string(value.InputSource) != "current_input" {
			return result, &apperrors.Business{Status: 400, Code: "INPUT_SOURCE_INVALID", Message: "one_shot applies to current_input only."}
		}
		result.OneShot = true
	}
	if value.Observation != nil {
		if string(value.InputSource) != "current_input" {
			return result, &apperrors.Business{Status: 400, Code: "INPUT_SOURCE_INVALID", Message: "observation applies to current_input only."}
		}
		result.Observation = &readmodelapplication.RSSListObservationRef{
			Provider:         string(value.Observation.Provider),
			ProviderEntryRef: value.Observation.ProviderEntryRef,
			SnapshotVersion:  int64(value.Observation.SnapshotVersion),
		}
	}
	switch string(value.InputSource) {
	case "current_input":
		if value.Draft == nil || value.ManagedAnimeId != nil || value.RulesPreview != nil || value.MetadataPreview != nil {
			return result, &apperrors.Business{Status: 400, Code: "INPUT_SOURCE_INVALID", Message: "current_input requires draft and forbids other mode fields."}
		}
		// candidate_id + revision on current_input name the saved custom preset
		// revision whose source snapshot a custom draft's bench reads; they go
		// together, and the read model accepts them for a custom draft only.
		if (value.CandidateId == nil) != (value.Revision == nil) {
			return result, &apperrors.Business{Status: 400, Code: "INPUT_SOURCE_INVALID", Message: "current_input candidate_id and revision go together."}
		}
		draft, err := mapAnimeDraft(*value.Draft)
		if err != nil {
			return result, err
		}
		result.Draft = &draft
		if value.CandidateId != nil {
			result.CandidateID = string(*value.CandidateId)
			result.Revision = int64(*value.Revision)
		}
	case "candidate_revision":
		if value.CandidateId == nil || value.Revision == nil || value.Draft != nil || value.ManagedAnimeId != nil || value.RulesPreview != nil || value.MetadataPreview != nil {
			return result, &apperrors.Business{Status: 400, Code: "INPUT_SOURCE_INVALID", Message: "candidate_revision requires candidate_id and revision."}
		}
		result.CandidateID = string(*value.CandidateId)
		result.Revision = int64(*value.Revision)
	case "owner", "owner_draft":
		if value.ManagedAnimeId == nil || value.Draft != nil || value.CandidateId != nil || value.Revision != nil {
			return result, &apperrors.Business{Status: 400, Code: "INPUT_SOURCE_INVALID", Message: "owner modes require managed_anime_id and forbid draft or candidate fields."}
		}
		result.ManagedAnimeID = string(*value.ManagedAnimeId)
		if string(value.InputSource) == "owner_draft" {
			if value.RulesPreview == nil && value.MetadataPreview == nil {
				return result, &apperrors.Business{Status: 400, Code: "INPUT_SOURCE_INVALID", Message: "owner_draft requires rules_preview, metadata_preview, or both."}
			}
			if value.RulesPreview != nil {
				preview, err := mapRulesPreviewInput(*value.RulesPreview)
				if err != nil {
					return result, err
				}
				result.RulesPreview = &preview
			}
			if value.MetadataPreview != nil {
				preview := mapMetadataPreviewInput(*value.MetadataPreview)
				result.MetadataPreview = &preview
			}
		} else if value.RulesPreview != nil || value.MetadataPreview != nil {
			return result, &apperrors.Business{Status: 400, Code: "INPUT_SOURCE_INVALID", Message: "owner forbids rules_preview and metadata_preview."}
		}
	default:
		return result, &apperrors.Business{Status: 400, Code: "INPUT_SOURCE_INVALID", Message: "input_source is invalid."}
	}
	return result, nil
}

// mapSnapshotSeedDownloadInput projects one snapshot-seed download request. The
// path carries the exact snapshot and seed identity; the body carries the
// configuration identity and the optional manual file selection.
func mapSnapshotSeedDownloadInput(snapshotID, stableSeedKey string, value generated.SnapshotSeedDownloadInput) (oneshotapplication.SnapshotSeedDownloadInput, error) {
	if strings.TrimSpace(snapshotID) == "" || strings.TrimSpace(stableSeedKey) == "" {
		return oneshotapplication.SnapshotSeedDownloadInput{}, &apperrors.Business{Status: 400, Code: "SEED_IDENTITY_REQUIRED", Message: "snapshot_id and stable_seed_key are required."}
	}
	input := oneshotapplication.SnapshotSeedDownloadInput{
		Provider:         string(value.Provider),
		ProviderEntryRef: value.ProviderEntryRef,
		ProviderGroupRef: value.ProviderGroupRef,
		SnapshotID:       snapshotID,
		SnapshotVersion:  int64(value.SnapshotVersion),
		ContentHash:      value.ContentHash,
		StableSeedKey:    stableSeedKey,
		InputSource:      string(value.InputSource),
	}
	if value.EpisodeOffset != nil {
		input.EpisodeOffset = *value.EpisodeOffset
	}
	switch string(value.InputSource) {
	case "current_input":
		if value.Draft == nil {
			return oneshotapplication.SnapshotSeedDownloadInput{}, &apperrors.Business{Status: 400, Code: "INPUT_SOURCE_INVALID", Message: "input_source=current_input requires a draft."}
		}
		if value.CandidateId != nil || value.Revision != nil {
			return oneshotapplication.SnapshotSeedDownloadInput{}, &apperrors.Business{Status: 400, Code: "INPUT_SOURCE_INVALID", Message: "input_source=current_input forbids candidate_id and revision."}
		}
		draft, err := mapAnimeDraft(*value.Draft)
		if err != nil {
			return oneshotapplication.SnapshotSeedDownloadInput{}, err
		}
		input.Draft = &draft
	case "candidate_revision":
		if value.CandidateId == nil || value.Revision == nil {
			return oneshotapplication.SnapshotSeedDownloadInput{}, &apperrors.Business{Status: 400, Code: "INPUT_SOURCE_INVALID", Message: "input_source=candidate_revision requires candidate_id and revision."}
		}
		if value.Draft != nil {
			return oneshotapplication.SnapshotSeedDownloadInput{}, &apperrors.Business{Status: 400, Code: "INPUT_SOURCE_INVALID", Message: "input_source=candidate_revision forbids draft."}
		}
		input.CandidateID = string(*value.CandidateId)
		input.Revision = int64(*value.Revision)
	default:
		return oneshotapplication.SnapshotSeedDownloadInput{}, &apperrors.Business{Status: 400, Code: "INPUT_SOURCE_INVALID", Message: "input_source must be current_input or candidate_revision."}
	}
	return input, nil
}

func mapRSSListView(view readmodelapplication.RSSListView) (generated.RSSListView, error) {
	hold := mapMetadataHold(view.MetadataHold)
	groups := make([]generated.RSSListGroup, 0, len(view.Groups))
	for _, group := range view.Groups {
		items := make([]generated.RSSListItem, 0, len(group.Items))
		for _, item := range group.Items {
			// Every item carries a verdict, and only the read model decides which
			// one. An empty verdict would serialize as an unknown enum member, so it
			// is refused loudly instead of being sent as a silent contract break.
			if item.LibraryOverlap.Verdict == "" {
				return generated.RSSListView{}, fmt.Errorf("rss list item %s has no library overlap verdict", item.StableSeedKey)
			}
			items = append(items, mapRSSListItem(item))
		}
		groups = append(groups, generated.RSSListGroup{
			Scope:       generated.RSSListGroupScope{Provider: generated.SubscriptionOrigin(group.Scope.Provider), ProviderEntryRef: group.Scope.ProviderEntryRef, ProviderGroupRef: group.Scope.ProviderGroupRef, RssSourceId: optionalRSSListID(group.Scope.RSSSourceID)},
			DisplayName: group.DisplayName, Score: cloneIntPtr(&group.Score),
			Selection: generated.RSSListGroupSelection{Source: mapRSSListSelectionSource(group.Selection.Source), EffectiveSelected: group.Selection.EffectiveSelected, SelectionMode: generated.SelectionMode(group.Selection.SelectionMode), CountInLimit: group.Selection.CountInLimit, ReasonCode: cloneStringPtr(group.Selection.ReasonCode)},
			Offset:    mapRSSListOffset(group.Offset), Feed: mapRSSListFeedStatus(group.Feed), Items: items,
		})
	}
	unused := make([]generated.PreviewFeedSnapshotRef, 0, len(view.UnusedFeedIdentities))
	for _, ref := range view.UnusedFeedIdentities {
		unused = append(unused, generated.PreviewFeedSnapshotRef{Provider: generated.PreviewFeedSnapshotRefProvider(ref.Provider), ProviderEntryRef: ref.ProviderEntryRef, ProviderGroupRef: ref.ProviderGroupRef, SnapshotId: ref.SnapshotID, SnapshotVersion: generated.Version(ref.SnapshotVersion), ContentHash: ref.ContentHash})
	}
	return generated.RSSListView{
		Mode: generated.RSSListViewMode(view.Mode), ConfigIdentity: view.ConfigIdentity, MetadataHold: hold,
		LimitPerGroup: view.LimitPerGroup, Truncated: view.Truncated, UnusedFeedIdentities: unused,
		Versions:    generated.RSSListViewVersions{Parser: view.Versions.Parser, Evaluator: view.Versions.Evaluator, Renderer: view.Versions.Renderer, Safety: view.Versions.Safety, TorrentParser: view.Versions.TorrentParser},
		GeneratedAt: view.GeneratedAt, Groups: groups, Observation: mapRSSListObservation(view.Observation),
	}, nil
}

// mapRSSListSelectionSource defaults an unclassified group to configured: only the
// observation path classifies a group as observed.
func mapRSSListSelectionSource(source string) generated.RSSListGroupSelectionSource {
	if source == readmodelapplication.RSSListSelectionSourceObservedDefault {
		return generated.RSSListGroupSelectionSource(source)
	}
	return generated.RSSListGroupSelectionSource(readmodelapplication.RSSListSelectionSourceConfigured)
}

func mapRSSListObservation(observation *readmodelapplication.RSSListObservation) *generated.RSSListObservation {
	if observation == nil {
		return nil
	}
	return &generated.RSSListObservation{
		State:            generated.RSSListObservationState(observation.State),
		Provider:         generated.RSSListObservationProvider(observation.Provider),
		ProviderEntryRef: observation.ProviderEntryRef,
		SnapshotVersion:  generated.Version(observation.SnapshotVersion),
		ObservedAt:       observation.ObservedAt,
		GroupCount:       observation.GroupCount,
	}
}

func optionalRSSListID(value string) *generated.ID {
	if value == "" {
		return nil
	}
	id := generated.ID(value)
	return &id
}
func mapRSSListOffset(offset readmodelapplication.RSSListOffset) generated.RSSListOffset {
	return generated.RSSListOffset{Value: offset.Value, Editable: offset.Editable, Source: generated.RSSListOffsetSource(offset.Source)}
}

func mapRSSListFeedStatus(status readmodelapplication.RSSListFeedStatus) generated.RSSListFeedStatus {
	mapped := generated.RSSListFeedStatus{
		State: generated.RSSListFeedStatusState(status.State),
	}
	if status.SnapshotID != "" {
		id := generated.ID(status.SnapshotID)
		mapped.SnapshotId = &id
	}
	if status.SnapshotVersion > 0 {
		version := generated.Version(status.SnapshotVersion)
		mapped.SnapshotVersion = &version
	}
	if status.ContentHash != "" {
		hash := status.ContentHash
		mapped.ContentHash = &hash
	}
	if status.ErrorCode != "" {
		code := status.ErrorCode
		mapped.ErrorCode = &code
	}
	if status.ErrorReason != "" {
		reason := status.ErrorReason
		mapped.ErrorReason = &reason
	}
	return mapped
}

func mapRSSListItem(item readmodelapplication.RSSListItem) generated.RSSListItem {
	files := make([]generated.RSSListFile, 0, len(item.Files))
	for _, file := range item.Files {
		files = append(files, mapRSSListFile(file))
	}
	mapped := generated.RSSListItem{
		SnapshotId:    generated.ID(item.SnapshotID),
		StableSeedKey: item.StableSeedKey,
		Seed:          mapRSSListSeedFacts(item.Seed),
		Torrent:       mapRSSListTorrentState(item.Torrent),
		Evaluation:    mapRSSListEvaluation(item.Evaluation),
		Files:         files,
		DownloadAction: generated.RSSListDownloadAction{
			Allowed: item.DownloadAction.Allowed,
			Kind:    generated.RSSListDownloadActionKind(item.DownloadAction.Kind),
		},
	}
	if item.DownloadAction.ReasonCode != "" {
		code := item.DownloadAction.ReasonCode
		mapped.DownloadAction.ReasonCode = &code
	}
	if item.DownloadAction.Reason != "" {
		reason := item.DownloadAction.Reason
		mapped.DownloadAction.Reason = &reason
	}
	mapped.LibraryOverlap = mapRSSListLibraryOverlap(item.LibraryOverlap)
	return mapped
}

// mapRSSListLibraryOverlap maps the read-only overlap fact. An undetermined
// verdict withholds both counts (nil, never 0) so a browser cannot read
// "undecidable" as "nothing is held".
func mapRSSListLibraryOverlap(overlap readmodelapplication.LibraryOverlap) generated.LibraryOverlap {
	mapped := generated.LibraryOverlap{Verdict: generated.LibraryOverlapVerdict(overlap.Verdict)}
	if overlap.PresentFiles != nil {
		present := *overlap.PresentFiles
		mapped.PresentFiles = &present
	}
	if overlap.PendingFiles != nil {
		pending := *overlap.PendingFiles
		mapped.PendingFiles = &pending
	}
	if overlap.ReasonCode != nil && *overlap.ReasonCode != "" {
		code := *overlap.ReasonCode
		mapped.ReasonCode = &code
	}
	return mapped
}

func mapRSSListSeedFacts(seed readmodelapplication.RSSListSeedFacts) generated.RSSListSeedFacts {
	mapped := generated.RSSListSeedFacts{
		Title:          seed.Title,
		LocatorDisplay: seed.LocatorDisplay,
		SizeBytes:      seed.SizeBytes,
	}
	if seed.PublishedAt != nil {
		published := seed.PublishedAt.UTC()
		mapped.PublishedAt = &published
	}
	if seed.InfoHash != "" {
		hash := seed.InfoHash
		mapped.InfoHash = &hash
	}
	if seed.ExpectedPayloadDigest != "" {
		digest := seed.ExpectedPayloadDigest
		mapped.ExpectedPayloadDigest = &digest
	}
	if seed.AniBTTags != nil {
		tags := append([]string{}, (*seed.AniBTTags)...)
		mapped.AnibtTags = &tags
	}
	if seed.TitleTags != nil {
		tags := append([]string{}, (*seed.TitleTags)...)
		mapped.TitleTags = &tags
	}
	return mapped
}

func mapRSSListTorrentState(state readmodelapplication.RSSListTorrentState) generated.RSSListTorrentState {
	mapped := generated.RSSListTorrentState{State: generated.RSSListTorrentStateState(state.State)}
	if state.ErrorCode != "" {
		code := state.ErrorCode
		mapped.ErrorCode = &code
	}
	if state.ErrorReason != "" {
		reason := state.ErrorReason
		mapped.ErrorReason = &reason
	}
	if state.Manifest != nil {
		manifest := mapTorrentManifestView(*state.Manifest)
		mapped.Manifest = &manifest
	}
	return mapped
}

func mapTorrentManifestView(manifest animecatalogdomain.TorrentManifest) generated.TorrentManifestView {
	mapped := generated.TorrentManifestView{
		PayloadDigest:    manifest.PayloadDigest,
		ParserVersion:    manifest.ParserVersion,
		TorrentVersion:   generated.TorrentManifestViewTorrentVersion(manifest.TorrentVersion),
		TorrentName:      manifest.TorrentName,
		TotalSizeBytes:   manifest.TotalSizeBytes,
		FileCount:        manifest.FileCount(),
		ProjectionDigest: manifest.ProjectionDigest,
	}
	if manifest.InfoHashV1 != "" {
		hash := manifest.InfoHashV1
		mapped.InfoHashV1 = &hash
	}
	if manifest.InfoHashV2 != "" {
		hash := manifest.InfoHashV2
		mapped.InfoHashV2 = &hash
	}
	return mapped
}

func mapRSSListEvaluation(evaluation readmodelapplication.RSSListEvaluation) generated.RSSListEvaluation {
	mapped := generated.RSSListEvaluation{
		State:       generated.RSSListEvaluationState(evaluation.State),
		ParsedFacts: mapRulesPreviewParsedFacts(evaluation.ParsedFacts),
	}
	if evaluation.Verdict != "" {
		verdict := generated.ReleaseVerdict(evaluation.Verdict)
		mapped.Verdict = &verdict
	}
	if evaluation.ReasonCode != "" {
		code := evaluation.ReasonCode
		mapped.ReasonCode = &code
	}
	if evaluation.ReasonDetail != "" {
		detail := evaluation.ReasonDetail
		mapped.ReasonDetail = &detail
	}
	return mapped
}

func mapRSSListFile(file readmodelapplication.RSSListFile) generated.RSSListFile {
	mapped := generated.RSSListFile{
		FileRef:    file.FileRef,
		Ordinal:    file.Ordinal,
		SourcePath: file.SourcePath,
		Filename:   file.Filename,
		SizeBytes:  file.SizeBytes,
		Included:   file.Included,
		Safety:     generated.RSSListFileSafety(file.Safety),
	}
	// An unknown size or kind is withheld rather than published as 0 or an
	// empty enum value: the contract's media_kind admits only video, subtitle
	// and other, and the reader degrades to the file's own extension.
	if file.MediaKind != nil {
		kind := generated.RSSListFileMediaKind(*file.MediaKind)
		mapped.MediaKind = &kind
	}
	if file.Language != "" {
		language := file.Language
		mapped.Language = &language
	}
	if file.ReasonCode != "" {
		code := file.ReasonCode
		mapped.ReasonCode = &code
	}
	if file.RenderedTarget != "" {
		target := file.RenderedTarget
		mapped.RenderedTarget = &target
	}
	if len(file.EmptyFacts) > 0 {
		facts := append([]string(nil), file.EmptyFacts...)
		mapped.EmptyFacts = &facts
	}
	if file.RenderError != "" {
		renderError := file.RenderError
		mapped.RenderError = &renderError
	}
	if file.BoundVideoSourcePath != "" {
		boundVideo := file.BoundVideoSourcePath
		mapped.BoundVideoSourcePath = &boundVideo
	}
	if file.SubLang != "" {
		subLang := file.SubLang
		mapped.SubLang = &subLang
		if file.SubLangSource != "" {
			source := generated.RSSListFileSubLangSource(file.SubLangSource)
			mapped.SubLangSource = &source
		}
	}
	if file.SubLangTag != "" {
		tag := file.SubLangTag
		mapped.SubLangTag = &tag
	}
	// A nil fact stays JSON null: an undecidable file is never reported false.
	if file.InLibrary != nil {
		inLibrary := *file.InLibrary
		mapped.InLibrary = &inLibrary
	}
	if file.LibraryPath != nil {
		current := *file.LibraryPath
		mapped.LibraryPath = &current
		if file.LibraryNewPath != nil {
			next := *file.LibraryNewPath
			mapped.LibraryNewPath = &next
		}
	}
	return mapped
}
