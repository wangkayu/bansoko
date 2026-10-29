package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"bansoko/internal/transport/httpapi/generated"
)

// readDraftFile loads the full draft JSON (the same SubscribeAnimeInput shape
// accepted by current_input RSSListView) from a file.
func readDraftFile(path string) (generated.SubscribeAnimeInput, error) {
	var input generated.SubscribeAnimeInput
	raw, err := os.ReadFile(path)
	if err != nil {
		return input, usageErrorf("read draft file: %w", err)
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return input, usageErrorf("decode draft file (must match SubscribeAnimeInput): %w", err)
	}
	if input.Draft.Title == "" && input.Draft.Origin == "" {
		return input, newUsageError("draft file must contain a draft object with at least a title")
	}
	return input, nil
}

func cmdLogin(args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	endpoint := fs.String("endpoint", "", "API origin")
	username := fs.String("username", "", "owner username")
	password := fs.String("password", "", "owner password")
	if err := fs.Parse(args); err != nil {
		return parseError(err)
	}
	if strings.TrimSpace(*username) == "" || strings.TrimSpace(*password) == "" {
		return newUsageError("--username and --password are required")
	}
	origin := strings.TrimRight(*endpoint, "/")
	if origin == "" {
		origin = os.Getenv("BANSOKO_ENDPOINT")
	}
	if origin == "" {
		origin = "http://127.0.0.1:8080"
	}
	c := &client{endpoint: origin}
	// The CLI keeps its own credential store, so it asks for the token in the
	// body instead of the browser cookie transport (the default).
	transport := generated.LoginInputSessionTransportToken
	code, raw, err := c.do(context.Background(), "POST", "/auth/login",
		generated.LoginInput{Username: *username, Password: *password, SessionTransport: &transport}, nil)
	if err != nil {
		return err
	}
	if code != 200 {
		return fail(code, raw)
	}
	var result generated.LoginResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	if result.SessionToken == nil || *result.SessionToken == "" {
		return newUsageError("the server returned no session token (session_transport=token was requested)")
	}
	if err := writeToken(*result.SessionToken); err != nil {
		return err
	}
	if jsonMode {
		emitJSON(map[string]any{"logged_in": true, "username": result.Owner.Username})
		return nil
	}
	fmt.Fprintf(human(), "logged in as %s; session cached in ~/.config/bansoko/token\n", result.Owner.Username)
	return nil
}

func cmdList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cf := newCommonFlags(fs)
	if err := fs.Parse(args); err != nil {
		return parseError(err)
	}
	c, err := cf.resolve()
	if err != nil {
		return err
	}
	code, raw, err := c.do(context.Background(), "GET", "/anime", nil, nil)
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
	var items []generated.AnimeListItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return err
	}
	if len(items) == 0 {
		fmt.Fprintln(human(), "no subscribed anime")
		return nil
	}
	for _, item := range items {
		state := string(item.Subscription.State)
		hold := "held"
		if !item.MetadataHold.Held {
			hold = "ready"
		}
		fmt.Fprintf(human(), "%s\t%s\t%s\t%s\n", item.ManagedAnime.Id, item.ManagedAnime.Title, state, hold)
	}
	return nil
}

func cmdSearch(args []string) error {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cf := newCommonFlags(fs)
	provider := fs.String("provider", "anibt", "provider namespace: anibt or mikan")
	query := fs.String("query", "", "search keyword")
	if err := fs.Parse(args); err != nil {
		return parseError(err)
	}
	if strings.TrimSpace(*query) == "" {
		return newUsageError("--query is required")
	}
	if *provider != "anibt" && *provider != "mikan" {
		return usageErrorf("unsupported provider %q", *provider)
	}
	c, err := cf.resolve()
	if err != nil {
		return err
	}
	code, raw, err := c.do(context.Background(), "GET", "/catalog/providers/"+*provider+"/search?q="+queryEscape(*query), nil, nil)
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
	var items []generated.CatalogItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return err
	}
	if len(items) == 0 {
		fmt.Fprintln(human(), "no results")
		return nil
	}
	for _, item := range items {
		id := ""
		if item.BgmId != nil {
			id = "bgm:" + *item.BgmId
		}
		fmt.Fprintf(human(), "%s\t%s\t%s\n", item.Title, item.ProviderEntryRef, id)
	}
	return nil
}

func cmdParseTitle(args []string) error {
	fs := flag.NewFlagSet("parse-title", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cf := newCommonFlags(fs)
	title := fs.String("title", "", "release title to parse")
	if err := fs.Parse(args); err != nil {
		return parseError(err)
	}
	if strings.TrimSpace(*title) == "" {
		return newUsageError("--title is required")
	}
	c, err := cf.resolve()
	if err != nil {
		return err
	}
	code, raw, err := c.do(context.Background(), "POST", "/parse/release-title",
		generated.ParseReleaseTitleInput{Title: *title}, nil)
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
	var result generated.ParseReleaseTitleResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	group := ""
	if result.Facts.GroupName != nil {
		group = *result.Facts.GroupName
	}
	episode := ""
	if result.Episode != nil && result.Episode.Matched {
		episode = strconv.FormatFloat(float64(result.Episode.EpisodeNumber), 'f', -1, 32)
	}
	fmt.Fprintf(human(), "group=%s quality=%s episode=%s parser=%s\n", group, stringOr(result.Facts.Quality, ""), episode, result.ParserVersion)
	return nil
}

func cmdParseTorrent(args []string) error {
	fs := flag.NewFlagSet("parse-torrent", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cf := newCommonFlags(fs)
	url := fs.String("url", "", "absolute HTTP(S) torrent URL")
	if err := fs.Parse(args); err != nil {
		return parseError(err)
	}
	if strings.TrimSpace(*url) == "" {
		return newUsageError("--url is required")
	}
	c, err := cf.resolve()
	if err != nil {
		return err
	}
	code, raw, err := c.do(context.Background(), "POST", "/parse/torrent-files",
		generated.ParseTorrentFilesInput{TorrentUrl: *url}, nil)
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
	var result generated.ParseTorrentFilesResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	fmt.Fprintf(human(), "name=%s info_hash=%s files=%d total_size=%d parser=%s\n",
		result.TorrentName, result.InfoHashV1, len(result.Files), result.TotalSizeBytes, result.ParserVersion)
	return nil
}

func cmdPreview(args []string) error {
	fs := flag.NewFlagSet("preview", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cf := newCommonFlags(fs)
	draftFile := fs.String("draft-file", "", "path to the SubscribeAnimeInput draft JSON")
	feedFlags := newPreviewFeedFlags(fs)
	if err := fs.Parse(args); err != nil {
		return parseError(err)
	}
	if strings.TrimSpace(*draftFile) == "" {
		return newUsageError("--draft-file is required")
	}
	draft, err := readDraftFile(*draftFile)
	if err != nil {
		return err
	}
	feed, err := feedFlags.build(fs)
	if err != nil {
		return err
	}
	c, err := cf.resolve()
	if err != nil {
		return err
	}
	code, raw, err := c.do(context.Background(), "POST", "/anime/rss-list-view",
		generated.RSSListViewInput{
			InputSource: generated.RSSListViewInputInputSourceCurrentInput,
			Draft:       &draft.Draft,
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

// --- candidate commands (P0.2: replaced direct subscribe with CAS candidate flow) ---

func cmdCandidate(args []string) error {
	if len(args) == 0 {
		return newUsageError("usage: bansoko candidate <list|get|create|patch|activate|preview|delete> [flags]")
	}
	switch args[0] {
	case "list":
		return cmdCandidateList(args[1:])
	case "get":
		return cmdCandidateGet(args[1:])
	case "create":
		return cmdCandidateCreate(args[1:])
	case "patch":
		return cmdCandidatePatch(args[1:])
	case "activate":
		return cmdCandidateActivate(args[1:])
	case "preview":
		return cmdCandidatePreview(args[1:])
	case "delete":
		return cmdCandidateDelete(args[1:])
	default:
		return usageErrorf("unsupported candidate command %q", args[0])
	}
}

func cmdCandidateList(args []string) error {
	fs := flag.NewFlagSet("candidate list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cf := newCommonFlags(fs)
	origin := fs.String("origin", "", "filter by origin (anibt|mikan|custom|oneshot)")
	ref := fs.String("ref", "", "filter by origin_entry_ref")
	if err := fs.Parse(args); err != nil {
		return parseError(err)
	}
	c, err := cf.resolve()
	if err != nil {
		return err
	}
	path := "/anime-config-candidates"
	q := []string{}
	if *origin != "" {
		q = append(q, "origin="+queryEscape(*origin))
	}
	if *ref != "" {
		q = append(q, "origin_entry_ref="+queryEscape(*ref))
	}
	if len(q) > 0 {
		path += "?" + strings.Join(q, "&")
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
	var result generated.AnimeConfigCandidateList
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	if len(result.Candidates) == 0 {
		fmt.Fprintln(human(), "no candidates")
		return nil
	}
	for _, item := range result.Candidates {
		activated := "-"
		if item.ActivatedManagedAnimeId != nil {
			activated = string(*item.ActivatedManagedAnimeId)
		}
		currentRevision := "-"
		if item.CurrentRevision != nil {
			currentRevision = strconv.FormatInt(*item.CurrentRevision, 10)
		}
		fmt.Fprintf(human(), "%s\tv%d\tcurrent_revision=%s\t%s\t%s\t%s\n", item.Id, item.Version, currentRevision, item.Origin, item.Title, activated)
	}
	return nil
}

func cmdCandidateGet(args []string) error {
	fs := flag.NewFlagSet("candidate get", flag.ContinueOnError)
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
	code, raw, err := c.do(context.Background(), "GET", "/anime-config-candidates/"+*id, nil, nil)
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
	var item generated.AnimeConfigCandidate
	if err := json.Unmarshal(raw, &item); err != nil {
		return err
	}
	activated := "-"
	if item.ActivatedManagedAnimeId != nil {
		activated = string(*item.ActivatedManagedAnimeId)
	}
	ref := "-"
	if item.OriginEntryRef != nil {
		ref = *item.OriginEntryRef
	}
	currentRevision := "-"
	if item.CurrentRevision != nil {
		currentRevision = strconv.FormatInt(*item.CurrentRevision, 10)
	}
	fmt.Fprintf(human(), "id=%s version=%d current_revision=%s origin=%s ref=%s title=%s activated=%s\n",
		item.Id, item.Version, currentRevision, item.Origin, ref, item.Title, activated)
	return nil
}

func cmdCandidateCreate(args []string) error {
	fs := flag.NewFlagSet("candidate create", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cf := newCommonFlags(fs)
	draftFile := fs.String("draft-file", "", "path to the AnimeDraft JSON")
	if err := fs.Parse(args); err != nil {
		return parseError(err)
	}
	if strings.TrimSpace(*draftFile) == "" {
		return newUsageError("--draft-file is required")
	}
	draft, err := readDraftFile(*draftFile)
	if err != nil {
		return err
	}
	c, err := cf.resolve()
	if err != nil {
		return err
	}
	code, raw, err := c.do(context.Background(), "POST", "/anime-config-candidates",
		generated.SaveAnimeConfigCandidateInput{Draft: draft.Draft}, nil)
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
	var item generated.AnimeConfigCandidate
	if err := json.Unmarshal(raw, &item); err != nil {
		return err
	}
	currentRevision := "-"
	if item.CurrentRevision != nil {
		currentRevision = strconv.FormatInt(*item.CurrentRevision, 10)
	}
	fmt.Fprintf(human(), "created candidate %s v%d current_revision=%s (%s)\n", item.Id, item.Version, currentRevision, item.Title)
	return nil
}

func cmdCandidatePatch(args []string) error {
	fs := flag.NewFlagSet("candidate patch", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cf := newCommonFlags(fs)
	id := fs.String("id", "", "candidate ID")
	draftFile := fs.String("draft-file", "", "path to the AnimeDraft JSON")
	version := fs.Int64("version", 0, "expected current version (If-Match)")
	if err := fs.Parse(args); err != nil {
		return parseError(err)
	}
	if strings.TrimSpace(*id) == "" {
		return newUsageError("--id is required")
	}
	if strings.TrimSpace(*draftFile) == "" {
		return newUsageError("--draft-file is required")
	}
	draft, err := readDraftFile(*draftFile)
	if err != nil {
		return err
	}
	c, err := cf.resolve()
	if err != nil {
		return err
	}
	code, raw, err := c.do(context.Background(), "PATCH", "/anime-config-candidates/"+*id,
		generated.PatchAnimeConfigCandidateInput{Draft: draft.Draft},
		map[string]string{"If-Match": strconv.FormatInt(*version, 10)})
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
	var item generated.AnimeConfigCandidate
	if err := json.Unmarshal(raw, &item); err != nil {
		return err
	}
	currentRevision := "-"
	if item.CurrentRevision != nil {
		currentRevision = strconv.FormatInt(*item.CurrentRevision, 10)
	}
	fmt.Fprintf(human(), "patched candidate %s v%d current_revision=%s (%s)\n", item.Id, item.Version, currentRevision, item.Title)
	return nil
}

func cmdCandidateActivate(args []string) error {
	fs := flag.NewFlagSet("candidate activate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cf := newCommonFlags(fs)
	id := fs.String("id", "", "candidate ID")
	revision := fs.Int64("revision", 0, "exact saved candidate revision (immutable execution identity)")
	version := fs.Int64("version", 0, "expected current candidate version (If-Match CAS)")
	key := fs.String("key", "", "idempotency key (default: auto-generated and printed)")
	fs.StringVar(key, "idempotency-key", "", "alias for --key")
	state := fs.String("initial-state", "", "initial subscription state: pending (default), active or stopped")
	fs.StringVar(state, "state", "", "alias for --initial-state")
	start := fs.Bool("start", false, "start the subscription at once (same as --initial-state active)")
	if err := fs.Parse(args); err != nil {
		return parseError(err)
	}
	if strings.TrimSpace(*id) == "" {
		return newUsageError("--id is required")
	}
	if *revision < 1 {
		return newUsageError("--revision is required and must be >= 1; current_revision (execution identity) is different from version (If-Match CAS)")
	}
	idempotencyKey := *key
	if strings.TrimSpace(idempotencyKey) == "" {
		idempotencyKey = newID()
		fmt.Fprintf(os.Stderr, "idempotency-key: %s\n", idempotencyKey)
	}
	body := generated.CandidateSubscribeInput{Revision: *revision}
	switch *state {
	case "":
	case "pending", "active", "stopped":
		initial := generated.SubscriptionState(*state)
		body.InitialState = &initial
	default:
		return usageErrorf("--initial-state must be pending, active or stopped, got %q", *state)
	}
	if *start {
		body.Start = start
	}
	c, err := cf.resolve()
	if err != nil {
		return err
	}
	code, raw, err := c.do(context.Background(), "POST", "/anime-config-candidates/"+*id+"/subscribe",
		body,
		map[string]string{
			"If-Match":        strconv.FormatInt(*version, 10),
			"Idempotency-Key": idempotencyKey,
		})
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
	var result generated.SubscribeResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	fmt.Fprintf(human(), "activated %s revision=%d -> anime %s (subscription %s, %d rss sources)\n",
		*id, *revision, result.ManagedAnime.Id, result.Subscription.Id, len(result.RssSources))
	return nil
}

func cmdRefresh(args []string) error {
	fs := flag.NewFlagSet("refresh", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cf := newCommonFlags(fs)
	sourceID := fs.String("source-id", "", "RSS source owner ID")
	if err := fs.Parse(args); err != nil {
		return parseError(err)
	}
	if strings.TrimSpace(*sourceID) == "" {
		return newUsageError("--source-id is required")
	}
	c, err := cf.resolve()
	if err != nil {
		return err
	}
	code, raw, err := c.do(context.Background(), "POST", "/rss-sources/"+*sourceID+"/refresh", nil,
		map[string]string{"Idempotency-Key": newID()})
	if err != nil {
		return err
	}
	if code != 202 {
		return fail(code, raw)
	}
	if cf.json {
		printRaw(raw)
		return nil
	}
	var result generated.RSSRefreshAccepted
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	fmt.Fprintf(human(), "refresh accepted; work_item=%s\n", result.WorkItemId)
	return nil
}

func cmdRetryRelease(args []string) error {
	fs := flag.NewFlagSet("retry-release", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cf := newCommonFlags(fs)
	releaseID := fs.String("release-id", "", "release owner ID")
	if err := fs.Parse(args); err != nil {
		return parseError(err)
	}
	if strings.TrimSpace(*releaseID) == "" {
		return newUsageError("--release-id is required")
	}
	c, err := cf.resolve()
	if err != nil {
		return err
	}
	code, raw, err := c.do(context.Background(), "POST", "/releases/"+*releaseID+"/retry", nil,
		map[string]string{"Idempotency-Key": newID()})
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
	var result generated.ReleaseRetryResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	switch {
	case result.DownloadTaskId != nil:
		fmt.Fprintf(human(), "release %s %s; download task %s\n", result.ReleaseId, result.Outcome, *result.DownloadTaskId)
	case result.ReasonCode != nil:
		fmt.Fprintf(human(), "release %s %s: %s\n", result.ReleaseId, result.Outcome, *result.ReasonCode)
	default:
		fmt.Fprintf(human(), "release %s %s\n", result.ReleaseId, result.Outcome)
	}
	return nil
}

func cmdRetryImport(args []string) error {
	fs := flag.NewFlagSet("retry-import", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cf := newCommonFlags(fs)
	acquisitionID := fs.String("acquisition-id", "", "acquisition (execution record) ID")
	version := fs.Int64("version", 0, "the record version you read (get_execution_record)")
	key := fs.String("key", "", "idempotency key (default: a new key)")
	if err := fs.Parse(args); err != nil {
		return parseError(err)
	}
	if strings.TrimSpace(*acquisitionID) == "" {
		return newUsageError("--acquisition-id is required")
	}
	if *version < 1 {
		return newUsageError("--version is required")
	}
	idempotencyKey := strings.TrimSpace(*key)
	if idempotencyKey == "" {
		idempotencyKey = newID()
	}
	c, err := cf.resolve()
	if err != nil {
		return err
	}
	code, raw, err := c.do(context.Background(), "POST", "/acquisitions/"+*acquisitionID+"/import/retry", nil,
		map[string]string{"Idempotency-Key": idempotencyKey, "If-Match": strconv.FormatInt(*version, 10)})
	if err != nil {
		return err
	}
	if code != 202 {
		return fail(code, raw)
	}
	if cf.json {
		printRaw(raw)
		return nil
	}
	var result generated.AcquisitionImportRetryResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	fmt.Fprintf(human(), "import of %s runs again (attempt %d: %d files, %d placements)\n", result.AcquisitionId, result.Attempt, result.RetriedFiles, result.ReopenedPlacements)
	return nil
}

func cmdCancelDownload(args []string) error {
	fs := flag.NewFlagSet("cancel-download", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cf := newCommonFlags(fs)
	acquisitionID := fs.String("acquisition-id", "", "acquisition (execution record) ID")
	version := fs.Int64("version", 0, "the record version you read (get_execution_record)")
	key := fs.String("key", "", "idempotency key (default: a new key)")
	if err := fs.Parse(args); err != nil {
		return parseError(err)
	}
	if strings.TrimSpace(*acquisitionID) == "" {
		return newUsageError("--acquisition-id is required")
	}
	if *version < 1 {
		return newUsageError("--version is required")
	}
	idempotencyKey := strings.TrimSpace(*key)
	if idempotencyKey == "" {
		idempotencyKey = newID()
	}
	c, err := cf.resolve()
	if err != nil {
		return err
	}
	code, raw, err := c.do(context.Background(), "POST", "/acquisitions/"+*acquisitionID+"/download/cancel", nil,
		map[string]string{"Idempotency-Key": idempotencyKey, "If-Match": strconv.FormatInt(*version, 10)})
	if err != nil {
		return err
	}
	if code != 202 {
		return fail(code, raw)
	}
	if cf.json {
		printRaw(raw)
		return nil
	}
	var result generated.AcquisitionDownloadCancelResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	fmt.Fprintf(human(), "download of %s is cancelled (version %d); the record ends cancelled once the download worker acts\n", result.AcquisitionId, result.Version)
	return nil
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cf := newCommonFlags(fs)
	animeID := fs.String("anime-id", "", "managed anime ID")
	if err := fs.Parse(args); err != nil {
		return parseError(err)
	}
	if strings.TrimSpace(*animeID) == "" {
		return newUsageError("--anime-id is required")
	}
	c, err := cf.resolve()
	if err != nil {
		return err
	}
	code, raw, err := c.do(context.Background(), "GET", "/anime/"+*animeID+"/workspace", nil, nil)
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
	var ws generated.AnimeWorkspace
	if err := json.Unmarshal(raw, &ws); err != nil {
		return err
	}
	fmt.Fprintf(human(), "anime %s %s subscription=%s\n", ws.ManagedAnime.Id, ws.ManagedAnime.Title, ws.Subscription.State)
	if ws.MetadataHold.Held {
		missing := make([]string, len(ws.MetadataHold.Missing))
		for i, m := range ws.MetadataHold.Missing {
			missing[i] = string(m)
		}
		fmt.Fprintf(human(), "  metadata hold: missing %s\n", strings.Join(missing, ","))
	}
	for _, source := range ws.RssSources {
		health := "unknown"
		for _, runtime := range ws.RssSourceRuntimes {
			if runtime.RssSourceId == source.Id {
				health = string(runtime.Health)
				break
			}
		}
		fmt.Fprintf(human(), "  rss %s %s health=%s\n", source.Id, source.GroupName, health)
	}
	for _, release := range ws.Releases {
		fmt.Fprintf(human(), "  release %s %s verdict=%s\n", release.Id, release.RawFacts.Title, release.Verdict)
	}
	for _, task := range ws.DownloadTasks {
		detail := ""
		if task.FailureReason != nil {
			detail = " failure=" + *task.FailureReason
		}
		fmt.Fprintf(human(), "  download %s state=%s%s\n", task.Id, task.State, detail)
	}
	for _, artifact := range ws.Artifacts {
		fmt.Fprintf(human(), "  artifact %s state=%s files=%d\n", artifact.Id, artifact.State, len(artifact.Files))
	}
	for _, op := range ws.FileOperations {
		fmt.Fprintf(human(), "  file-op %s %s state=%s\n", op.Id, op.Operation, op.State)
	}
	return nil
}

func cmdWait(args []string) error {
	fs := flag.NewFlagSet("wait", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cf := newCommonFlags(fs)
	animeID := fs.String("anime-id", "", "managed anime ID")
	interval := fs.Duration("interval", 2*time.Second, "poll interval")
	timeout := fs.Duration("timeout", 120*time.Second, "overall timeout")
	if err := fs.Parse(args); err != nil {
		return parseError(err)
	}
	if strings.TrimSpace(*animeID) == "" {
		return newUsageError("--anime-id is required")
	}
	if *interval <= 0 || *timeout <= 0 {
		return newUsageError("--interval and --timeout must be positive")
	}
	c, err := cf.resolve()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	seen := map[string]string{}
	deadline := time.Now().Add(*timeout)
	for {
		code, raw, err := c.do(ctx, "GET", "/anime/"+*animeID+"/workspace", nil, nil)
		if err != nil {
			if ctx.Err() != nil {
				return errWaitTimeout()
			}
			return err
		}
		if code != 200 {
			return fail(code, raw)
		}
		var ws generated.AnimeWorkspace
		if err := json.Unmarshal(raw, &ws); err != nil {
			return err
		}
		for _, task := range ws.DownloadTasks {
			key := "download:" + string(task.Id)
			if seen[key] != string(task.State) {
				seen[key] = string(task.State)
				fmt.Fprintf(human(), "[%s] download %s -> %s\n", time.Now().Format("15:04:05"), task.Id, task.State)
			}
		}
		for _, artifact := range ws.Artifacts {
			key := "artifact:" + string(artifact.Id)
			if seen[key] != string(artifact.State) {
				seen[key] = string(artifact.State)
				fmt.Fprintf(human(), "[%s] artifact %s -> %s\n", time.Now().Format("15:04:05"), artifact.Id, artifact.State)
			}
		}
		for _, op := range ws.FileOperations {
			key := "fileop:" + string(op.Id)
			if seen[key] != string(op.State) {
				seen[key] = string(op.State)
				fmt.Fprintf(human(), "[%s] file-op %s -> %s\n", time.Now().Format("15:04:05"), op.Id, op.State)
			}
		}
		// Terminal failure states stop the wait with a non-zero exit.
		for _, task := range ws.DownloadTasks {
			if task.State == "failed" || task.State == "waiting_retry" {
				reason := ""
				if task.FailureReason != nil {
					reason = " (" + *task.FailureReason + ")"
				}
				return &cliError{exit: exitOther, code: "PIPELINE_FAILED", message: fmt.Sprintf("download %s ended in %s%s", task.Id, task.State, reason)}
			}
		}
		done := true
		if len(ws.DownloadTasks) == 0 {
			done = false
		}
		for _, task := range ws.DownloadTasks {
			if task.State != "completed" {
				done = false
			}
		}
		for _, artifact := range ws.Artifacts {
			if artifact.State != "verified" {
				done = false
			}
		}
		for _, op := range ws.FileOperations {
			if op.State != "completed" {
				done = false
			}
		}
		if done && len(ws.FileOperations) > 0 {
			if cf.json {
				printRaw(raw)
				return nil
			}
			fmt.Fprintln(human(), "pipeline completed: download -> artifact verified -> file operations done")
			return nil
		}
		if time.Now().After(deadline) {
			return errWaitTimeout()
		}
		select {
		case <-ctx.Done():
			return errWaitTimeout()
		case <-time.After(*interval):
		}
	}
}

func cmdRetry(args []string) error {
	fs := flag.NewFlagSet("retry", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cf := newCommonFlags(fs)
	owner := fs.String("owner", "", "owner kind: rss, download, or artifact")
	ownerID := fs.String("owner-id", "", "owner ID (RSS source, download task, or artifact)")
	workItemID := fs.String("work-item-id", "", "failed work item ID (the owner row's failed_work_item_id)")
	reason := fs.String("reason", "manual retry via bansoko", "human-readable reason")
	if err := fs.Parse(args); err != nil {
		return parseError(err)
	}
	if strings.TrimSpace(*ownerID) == "" || strings.TrimSpace(*workItemID) == "" {
		return newUsageError("--owner-id and --work-item-id are required (read failed_work_item_id from 'status --json')")
	}
	path := map[string]string{
		"rss":      "/rss-source-runtimes/" + *ownerID + "/retry",
		"download": "/download-tasks/" + *ownerID + "/retry",
		"artifact": "/artifacts/" + *ownerID + "/retry",
	}[*owner]
	if path == "" {
		return newUsageError("--owner must be one of rss, download, artifact")
	}
	c, err := cf.resolve()
	if err != nil {
		return err
	}
	code, raw, err := c.do(context.Background(), "POST", path,
		generated.ManualRetryInput{FailedWorkItemId: generated.ID(*workItemID), Reason: *reason},
		map[string]string{"Idempotency-Key": newID()})
	if err != nil {
		return err
	}
	if code != 202 {
		return fail(code, raw)
	}
	if cf.json {
		printRaw(raw)
		return nil
	}
	var result generated.ManualRetryResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	fmt.Fprintf(human(), "retry accepted for %s %s\n", result.OwnerType, result.OwnerId)
	return nil
}

func queryEscape(s string) string {
	// URL-escape a query value without pulling in net/url for one call site.
	const hexChars = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') ||
			ch == '-' || ch == '_' || ch == '.' || ch == '~' {
			b.WriteByte(ch)
		} else {
			b.WriteByte('%')
			b.WriteByte(hexChars[ch>>4])
			b.WriteByte(hexChars[ch&0x0f])
		}
	}
	return b.String()
}

func stringOr(value *string, fallback string) string {
	if value == nil {
		return fallback
	}
	return *value
}

func cmdAPIToken(args []string) error {
	if len(args) == 0 {
		return newUsageError("usage: bansoko api-token <create|list|revoke> [flags]")
	}
	switch args[0] {
	case "create":
		return cmdAPITokenCreate(args[1:])
	case "list":
		return cmdAPITokenList(args[1:])
	case "revoke":
		return cmdAPITokenRevoke(args[1:])
	default:
		return usageErrorf("unsupported api-token command %q", args[0])
	}
}

func cmdAPITokenCreate(args []string) error {
	fs := flag.NewFlagSet("api-token create", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cf := newCommonFlags(fs)
	name := fs.String("name", "", "token name")
	scopeList := fs.String("scopes", "", "comma-separated scopes: read, write, secrets (default read,write; read is required)")
	if err := fs.Parse(args); err != nil {
		return parseError(err)
	}
	if strings.TrimSpace(*name) == "" {
		return newUsageError("--name is required")
	}
	input := generated.ApiTokenCreateInput{Name: *name}
	if strings.TrimSpace(*scopeList) != "" {
		var scopes generated.ApiTokenScopes
		for _, scope := range strings.Split(*scopeList, ",") {
			scopes = append(scopes, generated.ApiTokenScope(strings.TrimSpace(scope)))
		}
		input.Scopes = &scopes
	}
	c, err := cf.resolve()
	if err != nil {
		return err
	}
	code, raw, err := c.do(context.Background(), "POST", "/api-tokens", input, nil)
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
	var result generated.ApiTokenCreateResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	fmt.Fprintf(human(), "token id %s created with scopes %s. Raw token (encrypted at rest; keep this machine safe):\n%s\n", result.TokenId, joinScopes(result.Scopes), result.Token)
	return nil
}

func cmdAPITokenList(args []string) error {
	fs := flag.NewFlagSet("api-token list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cf := newCommonFlags(fs)
	if err := fs.Parse(args); err != nil {
		return parseError(err)
	}
	c, err := cf.resolve()
	if err != nil {
		return err
	}
	code, raw, err := c.do(context.Background(), "GET", "/api-tokens", nil, nil)
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
	var tokens []generated.ApiToken
	if err := json.Unmarshal(raw, &tokens); err != nil {
		return err
	}
	if len(tokens) == 0 {
		fmt.Fprintln(human(), "no api tokens")
		return nil
	}
	for _, token := range tokens {
		fmt.Fprintf(human(), "%s\t%s\t%s\t%s\n", token.Id, token.Name, joinScopes(token.Scopes), token.CreatedAt.Format(time.RFC3339))
	}
	return nil
}

func cmdAPITokenRevoke(args []string) error {
	fs := flag.NewFlagSet("api-token revoke", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cf := newCommonFlags(fs)
	id := fs.String("id", "", "token ID")
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
	code, raw, err := c.do(context.Background(), "DELETE", "/api-tokens/"+*id, nil, nil)
	if err != nil {
		return err
	}
	if code != 204 {
		return fail(code, raw)
	}
	if cf.json {
		emitJSON(map[string]any{"revoked": true, "id": *id})
		return nil
	}
	fmt.Fprintf(human(), "token %s revoked\n", *id)
	return nil
}

func errWaitTimeout() error {
	return &cliError{exit: exitOther, code: "WAIT_TIMEOUT", message: "timeout waiting for the pipeline to complete"}
}

func joinScopes(scopes generated.ApiTokenScopes) string {
	parts := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		parts = append(parts, string(scope))
	}
	return strings.Join(parts, ",")
}
