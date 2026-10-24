package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"bansoko/internal/transport/httpapi/generated"
)

type previewFeedFlags struct {
	jsonValue       string
	provider        string
	entryRef        string
	groupRef        string
	snapshotID      string
	snapshotVersion int64
	contentHash     string
}

func newPreviewFeedFlags(fs *flag.FlagSet) *previewFeedFlags {
	flags := &previewFeedFlags{}
	fs.StringVar(&flags.jsonValue, "feed-json", "", "feed snapshot identity JSON (an array or one object)")
	fs.StringVar(&flags.jsonValue, "feed", "", "alias for --feed-json")
	fs.StringVar(&flags.jsonValue, "feed-snapshot-json", "", "alias for --feed-json")
	fs.StringVar(&flags.provider, "feed-provider", "", "feed snapshot provider (anibt or mikan)")
	fs.StringVar(&flags.provider, "snapshot-provider", "", "alias for --feed-provider")
	fs.StringVar(&flags.entryRef, "feed-entry-ref", "", "feed snapshot provider entry reference")
	fs.StringVar(&flags.entryRef, "snapshot-entry-ref", "", "alias for --feed-entry-ref")
	fs.StringVar(&flags.groupRef, "feed-group-ref", "", "feed snapshot provider group reference")
	fs.StringVar(&flags.groupRef, "snapshot-group-ref", "", "alias for --feed-group-ref")
	fs.StringVar(&flags.snapshotID, "feed-snapshot-id", "", "feed snapshot ID")
	fs.StringVar(&flags.snapshotID, "snapshot-id", "", "alias for --feed-snapshot-id")
	fs.Int64Var(&flags.snapshotVersion, "feed-snapshot-version", 0, "feed snapshot version")
	fs.Int64Var(&flags.snapshotVersion, "snapshot-version", 0, "alias for --feed-snapshot-version")
	fs.StringVar(&flags.contentHash, "feed-content-hash", "", "feed snapshot content hash")
	fs.StringVar(&flags.contentHash, "snapshot-content-hash", "", "alias for --feed-content-hash")
	return flags
}

func (flags *previewFeedFlags) build(fs *flag.FlagSet) (*[]generated.PreviewFeedSnapshotRef, error) {
	if strings.TrimSpace(flags.jsonValue) != "" && previewFeedScalarSet(fs) {
		return nil, newUsageError("--feed-json cannot be combined with feed snapshot identity flags")
	}
	if strings.TrimSpace(flags.jsonValue) != "" {
		var feeds []generated.PreviewFeedSnapshotRef
		if err := json.Unmarshal([]byte(flags.jsonValue), &feeds); err != nil {
			var feed generated.PreviewFeedSnapshotRef
			if objectErr := json.Unmarshal([]byte(flags.jsonValue), &feed); objectErr != nil {
				return nil, usageErrorf("--feed-json must be a feed snapshot object or array: %w", err)
			}
			feeds = []generated.PreviewFeedSnapshotRef{feed}
		}
		return &feeds, nil
	}
	if !previewFeedScalarSet(fs) {
		return nil, nil
	}
	provider := strings.TrimSpace(flags.provider)
	entryRef := strings.TrimSpace(flags.entryRef)
	groupRef := strings.TrimSpace(flags.groupRef)
	snapshotID := strings.TrimSpace(flags.snapshotID)
	contentHash := strings.TrimSpace(flags.contentHash)
	if provider == "" || entryRef == "" || groupRef == "" || snapshotID == "" || flags.snapshotVersion < 1 || contentHash == "" {
		return nil, newUsageError("feed snapshot identity flags require --feed-provider, --feed-entry-ref, --feed-group-ref, --feed-snapshot-id, --feed-snapshot-version >= 1, and --feed-content-hash")
	}
	if provider != "anibt" && provider != "mikan" {
		return nil, usageErrorf("--feed-provider must be anibt or mikan, got %q", provider)
	}
	feeds := []generated.PreviewFeedSnapshotRef{{
		Provider:         generated.PreviewFeedSnapshotRefProvider(provider),
		ProviderEntryRef: entryRef,
		ProviderGroupRef: groupRef,
		SnapshotId:       generated.ID(snapshotID),
		SnapshotVersion:  generated.Version(flags.snapshotVersion),
		ContentHash:      contentHash,
	}}
	return &feeds, nil
}

func previewFeedScalarSet(fs *flag.FlagSet) bool {
	for _, name := range []string{
		"feed-provider", "snapshot-provider", "feed-entry-ref", "snapshot-entry-ref",
		"feed-group-ref", "snapshot-group-ref", "feed-snapshot-id", "snapshot-id",
		"feed-snapshot-version", "snapshot-version", "feed-content-hash", "snapshot-content-hash",
	} {
		if flagWasSet(fs, name) {
			return true
		}
	}
	return false
}

func flagWasSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(flag *flag.Flag) {
		if flag.Name == name {
			set = true
		}
	})
	return set
}

func parseAnimeDraftJSON(raw string) (generated.AnimeDraft, error) {
	var draft generated.AnimeDraft
	if err := json.Unmarshal([]byte(raw), &draft); err != nil {
		return draft, usageErrorf("draft JSON must match AnimeDraft: %w", err)
	}
	if strings.TrimSpace(draft.Title) == "" {
		var wrapped generated.SubscribeAnimeInput
		if err := json.Unmarshal([]byte(raw), &wrapped); err == nil && strings.TrimSpace(wrapped.Draft.Title) != "" {
			draft = wrapped.Draft
		}
	}
	if strings.TrimSpace(draft.Title) == "" && draft.Origin == "" {
		return draft, newUsageError("draft JSON must contain an AnimeDraft with at least a title")
	}
	return draft, nil
}

func readDraftJSONValue(raw string) (generated.AnimeDraft, error) {
	if strings.HasPrefix(raw, "@") {
		contents, err := os.ReadFile(strings.TrimPrefix(raw, "@"))
		if err != nil {
			return generated.AnimeDraft{}, usageErrorf("read draft JSON file: %w", err)
		}
		raw = string(contents)
	}
	return parseAnimeDraftJSON(raw)
}

func pathSegment(value string) string {
	return url.PathEscape(value)
}

func metadataSearchPath(provider, query string, limit int, limitSet bool) string {
	path := "/metadata/search?provider=" + queryEscape(provider) + "&query=" + queryEscape(query)
	if limitSet {
		path += "&limit=" + strconv.Itoa(limit)
	}
	return path
}

func resolveCommandIdempotencyKey(value string) (string, bool, error) {
	if strings.TrimSpace(value) != "" {
		return value, false, nil
	}
	if value != "" {
		return "", false, newUsageError("--idempotency-key cannot be blank")
	}
	return newID(), true, nil
}

func printRSSListView(raw []byte) error {
	var result generated.RSSListView
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	for _, group := range result.Groups {
		selection := "unselected"
		if group.Selection.EffectiveSelected {
			selection = "selected"
		}
		fmt.Fprintf(human(), "group %s [%s] feed=%s\n", group.DisplayName, selection, group.Feed.State)
		if group.Selection.ReasonCode != nil {
			fmt.Fprintf(human(), "  reason: %s\n", *group.Selection.ReasonCode)
		}
		for _, item := range group.Items {
			verdict := "unavailable"
			if item.Evaluation.Verdict != nil {
				verdict = string(*item.Evaluation.Verdict)
			}
			fmt.Fprintf(human(), "  seed %s: %s", item.Seed.Title, verdict)
			if item.Evaluation.ReasonCode != nil {
				fmt.Fprintf(human(), " (%s)", *item.Evaluation.ReasonCode)
			}
			if item.Evaluation.ReasonDetail != nil {
				fmt.Fprintf(human(), " — %s", *item.Evaluation.ReasonDetail)
			}
			fmt.Fprintln(human())
			for _, file := range item.Files {
				fileVerdict := "excluded"
				if file.Included {
					fileVerdict = "included"
				}
				fmt.Fprintf(human(), "    file %s: %s", file.Filename, fileVerdict)
				if file.ReasonCode != nil {
					fmt.Fprintf(human(), " (%s)", *file.ReasonCode)
				}
				if file.RenderError != nil {
					fmt.Fprintf(human(), " — %s", *file.RenderError)
				}
				fmt.Fprintln(human())
			}
		}
	}
	return nil
}

func cmdCandidatePreview(args []string) error {
	fs := flag.NewFlagSet("candidate preview", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cf := newCommonFlags(fs)
	id := fs.String("id", "", "candidate ID")
	revision := fs.Int64("revision", 0, "exact saved candidate revision")
	feedFlags := newPreviewFeedFlags(fs)
	if err := fs.Parse(args); err != nil {
		return parseError(err)
	}
	if strings.TrimSpace(*id) == "" {
		return newUsageError("--id is required")
	}
	if *revision < 1 {
		return newUsageError("--revision is required and must be >= 1")
	}
	feed, err := feedFlags.build(fs)
	if err != nil {
		return err
	}
	c, err := cf.resolve()
	if err != nil {
		return err
	}
	candidateID := generated.ID(*id)
	candidateRevision := generated.Version(*revision)
	code, raw, err := c.do(context.Background(), "POST", "/anime/rss-list-view", generated.RSSListViewInput{
		InputSource: generated.RSSListViewInputInputSourceCandidateRevision,
		CandidateId: &candidateID,
		Revision:    &candidateRevision,
		Feed:        feed,
	}, nil)
	if err != nil {
		return err
	}
	if code != 200 {
		return fail(code, raw)
	}
	if cf.json {
		printRaw(raw)
		return nil
	}
	return printRSSListView(raw)
}

func cmdCandidateDelete(args []string) error {
	fs := flag.NewFlagSet("candidate delete", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cf := newCommonFlags(fs)
	id := fs.String("id", "", "candidate ID")
	if err := fs.Parse(args); err != nil {
		return parseError(err)
	}
	if strings.TrimSpace(*id) == "" {
		return newUsageError("--id is required")
	}
	c, err := cf.resolve()
	if err != nil {
		return err
	}
	code, raw, err := c.do(context.Background(), "DELETE", "/anime-config-candidates/"+pathSegment(*id), nil, nil)
	if err != nil {
		return err
	}
	if code != 204 {
		return fail(code, raw)
	}
	if cf.json {
		emitJSON(map[string]any{"deleted": true, "id": *id})
		return nil
	}
	fmt.Fprintf(human(), "deleted candidate %s\n", *id)
	return nil
}

func cmdFeed(args []string) error {
	if len(args) == 0 {
		return newUsageError("usage: bansoko feed <groups|snapshot|refresh> [flags]")
	}
	switch args[0] {
	case "groups":
		return cmdFeedGroups(args[1:])
	case "snapshot":
		return cmdFeedSnapshot(args[1:])
	case "refresh":
		return cmdFeedRefresh(args[1:])
	default:
		return usageErrorf("unsupported feed command %q", args[0])
	}
}

type feedFlags struct {
	cf       *commonFlags
	provider string
	entryRef string
	groupRef string
}

func newFeedFlags(fs *flag.FlagSet) *feedFlags {
	flags := &feedFlags{}
	flags.cf = newCommonFlags(fs)
	fs.StringVar(&flags.provider, "provider", "", "provider namespace: anibt or mikan")
	fs.StringVar(&flags.entryRef, "entry-ref", "", "provider entry reference")
	fs.StringVar(&flags.groupRef, "group-ref", "", "provider group reference")
	return flags
}

func (flags *feedFlags) validate(requireGroup bool) error {
	if strings.TrimSpace(flags.provider) == "" {
		return newUsageError("--provider is required")
	}
	if flags.provider != "anibt" && flags.provider != "mikan" {
		return usageErrorf("--provider must be anibt or mikan, got %q", flags.provider)
	}
	if strings.TrimSpace(flags.entryRef) == "" {
		return newUsageError("--entry-ref is required")
	}
	if requireGroup && strings.TrimSpace(flags.groupRef) == "" {
		return newUsageError("--group-ref is required by the provider RSS snapshot contract")
	}
	return nil
}

func (flags *feedFlags) path() string {
	return "/catalog/providers/" + pathSegment(flags.provider) + "/entries/" + pathSegment(flags.entryRef) + "/rss"
}

func cmdFeedGroups(args []string) error {
	fs := flag.NewFlagSet("feed groups", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	flags := newFeedFlags(fs)
	if err := fs.Parse(args); err != nil {
		return parseError(err)
	}
	if err := flags.validate(false); err != nil {
		return err
	}
	c, err := flags.cf.resolve()
	if err != nil {
		return err
	}
	path := "/catalog/providers/" + pathSegment(flags.provider) + "/entries/" + pathSegment(flags.entryRef) + "/groups:discover"
	code, raw, err := c.do(context.Background(), "POST", path, nil, nil)
	if err != nil {
		return err
	}
	if code != 200 {
		return fail(code, raw)
	}
	if flags.cf.json {
		printRaw(raw)
		return nil
	}
	var result generated.ProviderGroupDiscoveryResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	fmt.Fprintf(human(), "provider=%s entry_ref=%s snapshot_version=%d groups=%d\n", result.Provider, result.ProviderEntryRef, result.SnapshotVersion, len(result.Groups))
	for _, group := range result.Groups {
		fmt.Fprintf(human(), "%s\t%s\t%s\n", group.GroupRef, group.DisplayName, group.FeedUrl)
	}
	return nil
}

func cmdFeedSnapshot(args []string) error {
	fs := flag.NewFlagSet("feed snapshot", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	flags := newFeedFlags(fs)
	if err := fs.Parse(args); err != nil {
		return parseError(err)
	}
	if err := flags.validate(true); err != nil {
		return err
	}
	c, err := flags.cf.resolve()
	if err != nil {
		return err
	}
	path := flags.path() + "?group_ref=" + queryEscape(flags.groupRef)
	code, raw, err := c.do(context.Background(), "GET", path, nil, nil)
	if err != nil {
		return err
	}
	if code != 200 {
		return fail(code, raw)
	}
	if flags.cf.json {
		printRaw(raw)
		return nil
	}
	return printFeedSnapshotSummary(raw)
}

func cmdFeedRefresh(args []string) error {
	fs := flag.NewFlagSet("feed refresh", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	flags := newFeedFlags(fs)
	if err := fs.Parse(args); err != nil {
		return parseError(err)
	}
	if err := flags.validate(true); err != nil {
		return err
	}
	c, err := flags.cf.resolve()
	if err != nil {
		return err
	}
	code, raw, err := c.do(context.Background(), "POST", flags.path(), generated.ProviderRSSPreviewInput{GroupRef: flags.groupRef}, nil)
	if err != nil {
		return err
	}
	if code != 200 {
		return fail(code, raw)
	}
	if flags.cf.json {
		printRaw(raw)
		return nil
	}
	return printFeedSnapshotSummary(raw)
}

func printFeedSnapshotSummary(raw []byte) error {
	var result generated.ProviderRSSPreviewResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	snapshotVersion := "-"
	if result.SnapshotVersion != nil {
		snapshotVersion = strconv.FormatInt(int64(*result.SnapshotVersion), 10)
	}
	fmt.Fprintf(human(), "provider=%s entry_ref=%s group_ref=%s freshness=%s snapshot_version=%s seeds=%d\n",
		result.Provider, result.ProviderEntryRef, result.GroupRef, result.Freshness, snapshotVersion, len(result.Seeds))
	return nil
}

func cmdOneShot(args []string) error {
	if len(args) == 0 {
		return newUsageError("usage: bansoko one-shot <acquire|get|attempts|attempt> [flags]")
	}
	switch args[0] {
	case "acquire":
		return cmdOneShotAcquire(args[1:])
	case "get":
		return cmdOneShotGet(args[1:])
	case "attempts":
		return cmdOneShotAttempts(args[1:])
	case "attempt":
		return cmdOneShotAttempt(args[1:])
	default:
		return usageErrorf("unsupported one-shot command %q", args[0])
	}
}

func cmdOneShotAcquire(args []string) error {
	fs := flag.NewFlagSet("one-shot acquire", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cf := newCommonFlags(fs)
	candidateID := fs.String("candidate-id", "", "saved candidate ID")
	revision := fs.Int64("revision", 0, "exact saved candidate revision")
	draftJSON := fs.String("draft-json", "", "complete unsaved AnimeDraft JSON (or @file)")
	seedJSON := fs.String("seed-json", "", "SelectedSeedInput JSON: a snapshot reference (snapshot_id, snapshot_version, snapshot_content_hash, stable_seed_key) or the locator form")
	episodeOffset := fs.Int("episode-offset", 0, "the one-shot's episode offset, -1000..1000 (default 0)")
	idempotencyKey := fs.String("idempotency-key", "", "idempotency key (default: auto-generated and printed)")
	if err := fs.Parse(args); err != nil {
		return parseError(err)
	}
	candidateMode := strings.TrimSpace(*candidateID) != "" || flagWasSet(fs, "revision")
	draftMode := strings.TrimSpace(*draftJSON) != ""
	if candidateMode == draftMode {
		return newUsageError("exactly one input mode is required: --candidate-id with --revision, or --draft-json")
	}
	if candidateMode {
		if strings.TrimSpace(*candidateID) == "" {
			return newUsageError("--candidate-id is required with --revision")
		}
		if *revision < 1 {
			return newUsageError("--revision is required with --candidate-id and must be >= 1")
		}
	}
	if strings.TrimSpace(*seedJSON) == "" {
		return newUsageError("--seed-json is required")
	}
	var seed generated.SelectedSeedInput
	if err := json.Unmarshal([]byte(*seedJSON), &seed); err != nil {
		return usageErrorf("--seed-json must be valid SelectedSeedInput JSON: %w", err)
	}
	key, generatedKey, err := resolveCommandIdempotencyKey(*idempotencyKey)
	if err != nil {
		return err
	}
	if generatedKey {
		fmt.Fprintf(os.Stderr, "idempotency-key: %s\n", key)
	}
	input := generated.AcquireSeedOnceInput{
		SelectedSeed: seed,
	}
	if flagWasSet(fs, "episode-offset") {
		offset := *episodeOffset
		input.EpisodeOffset = &offset
	}
	if candidateMode {
		source := generated.AcquireSeedOnceInputInputSource("saved_revision")
		input.InputSource = &source
		candidate := generated.ID(*candidateID)
		input.CandidateId = &candidate
		input.Revision = revision
	} else {
		source := generated.AcquireSeedOnceInputInputSource("current_input")
		input.InputSource = &source
		draft, err := readDraftJSONValue(*draftJSON)
		if err != nil {
			return err
		}
		input.Draft = &draft
	}
	c, err := cf.resolve()
	if err != nil {
		return err
	}
	code, raw, err := c.do(context.Background(), "POST", "/one-shot-acquisitions", input, map[string]string{"Idempotency-Key": key})
	if err != nil {
		return err
	}
	if code != 201 {
		return fail(code, raw)
	}
	if cf.json {
		printRaw(raw)
		return nil
	}
	var result generated.OneShotAdmissionResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	fmt.Fprintf(human(), "one-shot %s admitted state=%s anime=%s subscription=%s release=%s\n",
		result.OneShotId, result.State, result.ManagedAnimeId, result.SubscriptionId, result.ReleaseId)
	return nil
}

func cmdOneShotGet(args []string) error {
	fs := flag.NewFlagSet("one-shot get", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cf := newCommonFlags(fs)
	id := fs.String("id", "", "one-shot acquisition ID")
	if err := fs.Parse(args); err != nil {
		return parseError(err)
	}
	if strings.TrimSpace(*id) == "" {
		return newUsageError("--id is required")
	}
	c, err := cf.resolve()
	if err != nil {
		return err
	}
	code, raw, err := c.do(context.Background(), "GET", "/one-shot-acquisitions/"+pathSegment(*id), nil, nil)
	if err != nil {
		return err
	}
	if code != 200 {
		return fail(code, raw)
	}
	if cf.json {
		printRaw(raw)
		return nil
	}
	var view generated.OneShotAcquisitionView
	if err := json.Unmarshal(raw, &view); err != nil {
		return err
	}
	attempt := "-"
	if view.CurrentAttemptId != nil {
		attempt = string(*view.CurrentAttemptId)
	}
	fmt.Fprintf(human(), "one-shot %s state=%s version=%d current_attempt=%s\n", view.OneShotId, view.State, view.Version, attempt)
	return nil
}

func cmdOneShotAttempts(args []string) error {
	fs := flag.NewFlagSet("one-shot attempts", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cf := newCommonFlags(fs)
	id := fs.String("id", "", "one-shot acquisition ID")
	limit := fs.Int("limit", 0, "maximum attempts to return")
	cursor := fs.String("cursor", "", "opaque attempts pagination cursor")
	if err := fs.Parse(args); err != nil {
		return parseError(err)
	}
	if strings.TrimSpace(*id) == "" {
		return newUsageError("--id is required")
	}
	path := "/one-shot-acquisitions/" + pathSegment(*id) + "/attempts"
	query := make([]string, 0, 2)
	if flagWasSet(fs, "limit") {
		if *limit < 1 || *limit > 100 {
			return newUsageError("--limit must be between 1 and 100")
		}
		query = append(query, "limit="+strconv.Itoa(*limit))
	}
	if flagWasSet(fs, "cursor") {
		if strings.TrimSpace(*cursor) == "" {
			return newUsageError("--cursor cannot be blank")
		}
		query = append(query, "cursor="+queryEscape(*cursor))
	}
	if len(query) > 0 {
		path += "?" + strings.Join(query, "&")
	}
	c, err := cf.resolve()
	if err != nil {
		return err
	}
	code, raw, err := c.do(context.Background(), "GET", path, nil, nil)
	if err != nil {
		return err
	}
	if code != 200 {
		return fail(code, raw)
	}
	if cf.json {
		printRaw(raw)
		return nil
	}
	var page generated.OneShotExecutionAttemptPage
	if err := json.Unmarshal(raw, &page); err != nil {
		return err
	}
	fmt.Fprintf(human(), "one-shot %s attempts=%d\n", *id, len(page.Items))
	for _, item := range page.Items {
		fmt.Fprintf(human(), "%s\tseq=%d\tmode=%s	state=%s\n", item.Id, item.Sequence, item.Mode, item.State)
	}
	if page.NextCursor != nil {
		fmt.Fprintf(human(), "next_cursor=%s\n", *page.NextCursor)
	}
	return nil
}

func cmdOneShotAttempt(args []string) error {
	fs := flag.NewFlagSet("one-shot attempt", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cf := newCommonFlags(fs)
	id := fs.String("id", "", "one-shot acquisition ID")
	attemptID := fs.String("attempt-id", "", "execution attempt ID")
	if err := fs.Parse(args); err != nil {
		return parseError(err)
	}
	if strings.TrimSpace(*id) == "" {
		return newUsageError("--id is required")
	}
	if strings.TrimSpace(*attemptID) == "" {
		return newUsageError("--attempt-id is required")
	}
	c, err := cf.resolve()
	if err != nil {
		return err
	}
	path := "/one-shot-acquisitions/" + pathSegment(*id) + "/attempts/" + pathSegment(*attemptID)
	code, raw, err := c.do(context.Background(), "GET", path, nil, nil)
	if err != nil {
		return err
	}
	if code != 200 {
		return fail(code, raw)
	}
	if cf.json {
		printRaw(raw)
		return nil
	}
	var attempt generated.OneShotExecutionAttempt
	if err := json.Unmarshal(raw, &attempt); err != nil {
		return err
	}
	fmt.Fprintf(human(), "attempt %s acquisition=%s seq=%d mode=%s state=%s\n", attempt.Id, attempt.AcquisitionId, attempt.Sequence, attempt.Mode, attempt.State)
	return nil
}

func cmdMetadata(args []string) error {
	if len(args) == 0 {
		return newUsageError("usage: bansoko metadata search [flags]")
	}
	if args[0] != "search" {
		return usageErrorf("unsupported metadata command %q", args[0])
	}
	return cmdMetadataSearch(args[1:])
}

func cmdMetadataSearch(args []string) error {
	fs := flag.NewFlagSet("metadata search", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cf := newCommonFlags(fs)
	provider := fs.String("provider", "bgm", "metadata provider: bgm or tmdb")
	query := fs.String("q", "", "metadata search query")
	fs.StringVar(query, "query", "", "alias for --q")
	limit := fs.Int("limit", 0, "maximum candidates (1-50)")
	if err := fs.Parse(args); err != nil {
		return parseError(err)
	}
	if *provider != "bgm" && *provider != "tmdb" {
		return usageErrorf("--provider must be bgm or tmdb, got %q", *provider)
	}
	if strings.TrimSpace(*query) == "" {
		return newUsageError("--q is required")
	}
	if flagWasSet(fs, "limit") && (*limit < 1 || *limit > 50) {
		return newUsageError("--limit must be between 1 and 50")
	}
	c, err := cf.resolve()
	if err != nil {
		return err
	}
	path := metadataSearchPath(*provider, *query, *limit, flagWasSet(fs, "limit"))
	code, raw, err := c.do(context.Background(), "GET", path, nil, nil)
	if err != nil {
		return err
	}
	if code != 200 {
		return fail(code, raw)
	}
	if cf.json {
		printRaw(raw)
		return nil
	}
	var result generated.MetadataSearchResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	fmt.Fprintf(human(), "provider=%s query=%s candidates=%d\n", result.Provider, result.Query, len(result.Candidates))
	for _, candidate := range result.Candidates {
		fmt.Fprintf(human(), "%s\t%s\t%s\t%.3f\n", candidate.ProviderWorkId, candidate.Title, candidate.MediaKind, candidate.Score)
	}
	return nil
}
