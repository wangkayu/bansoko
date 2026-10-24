package application

import (
	acquisitiondomain "bansoko/internal/acquisition/domain"
	protocol "bansoko/internal/acquisition/terminal/domain"
	anime "bansoko/internal/anime/domain"
	"bansoko/internal/download/qbittorrent"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
	"bansoko/internal/worktree"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// workTreeTop returns the tree top of the work's recorded files: the deepest
// directory common to the current paths of its active placements. placed=false
// means the work has no placement evidence yet (typically its first
// acquisition), and the caller must not substitute a directory for it.
func workTreeTop(ctx context.Context, tx *sql.Tx, managedAnimeID string) (string, bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT physical_path FROM placements
WHERE managed_anime_id = $1 AND state = 'active' ORDER BY physical_path LIMIT 1000`, managedAnimeID)
	if err != nil {
		return "", false, fmt.Errorf("read work tree top: %w", err)
	}
	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			rows.Close()
			return "", false, fmt.Errorf("read work tree top: %w", err)
		}
		paths = append(paths, path)
	}
	if err := rows.Close(); err != nil {
		return "", false, fmt.Errorf("read work tree top: %w", err)
	}
	if err := rows.Err(); err != nil {
		return "", false, fmt.Errorf("read work tree top: %w", err)
	}
	top, ok := worktree.Top(paths)
	return top, ok, nil
}

// productionIntent is the sole production Media/Delivery/Limits projection.
// It reads actual transaction-local configuration; no fixture defaults, client
// proof, current-program substitution or invented filesystem roots are used.
func productionIntent(ctx context.Context, tx *sql.Tx, c admissionCapture, mode, connection string, overwriteTargetAbsent bool, captures ...*ReplacementCapture) (acquisitionIntent, error) {
	var title string
	var titleOverride, providerTitle sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT title, title_override, provider_title FROM managed_anime WHERE id=$1`, c.material.management.managedID).Scan(&title, &titleOverride, &providerTitle); err != nil {
		return acquisitionIntent{}, err
	}
	media := map[string]any{}
	for _, key := range []string{"title", "title_override", "provider_title", "bgm", "tmdb", "group", "identity", "quality"} {
		media[key] = frozenValue("absent", nil)
	}
	media["title"] = frozenValue("known", title)
	// The title override and the provider catalog title are frozen here, not
	// derived later: the title chain reads them from the typed plan, and the
	// override is a real chain member the user set at subscribe time.
	if override := strings.TrimSpace(titleOverride.String); override != "" {
		media["title_override"] = frozenValue("known", override)
	}
	if provider := strings.TrimSpace(providerTitle.String); provider != "" {
		media["provider_title"] = frozenValue("known", provider)
	}
	// Release-level quality is frozen with the acceptance: the same title and
	// the attribute rules captured by this qualification are what the
	// pre-download TargetSet rendered with, so typed planning reuses this
	// value instead of re-parsing under later rules (or losing it when the
	// downloaded file name carries no resolution marker).
	if seed, err := admissionSeed(c); err == nil && seed != nil {
		parsed := acquisitiondomain.ParseReleaseFacts(seed.Title, c.material.programs.attr)
		if parsed.Resolution != "" || parsed.Quality != "" || parsed.GroupName != "" || parsed.Language != "" {
			media["quality"] = frozenValue("known", replacementQuality(replacementFactsSnapshot{
				Group: parsed.GroupName, Resolution: parsed.Resolution, Source: parsed.Quality, Language: parsed.Language,
			}))
		}
	}
	if c.material.bgm != nil && strings.TrimSpace(c.material.bgm.WorkId) != "" {
		media["bgm"] = frozenValue("known", map[string]any{"ref": c.material.bgm.WorkId, "version": c.material.metadataVersion})
	}
	if c.material.tmdb != nil {
		b := c.material.tmdb
		media["tmdb"] = frozenValue("known", map[string]any{"work_ref": b.WorkId, "kind": b.MediaKind, "season": b.Season, "version": c.material.metadataVersion})
	}
	unavailable := func(reason string) any {
		return map[string]any{"state": "unavailable", "value": nil, "reason_code": reason}
	}
	// Placement targets are absolute: the naming template carries its own
	// deployment path. The delivery route root is the work's tree top — the
	// deepest directory its recorded placements put files in — so an unknown
	// file is routed inside the work's own tree; a work the product has never
	// placed has no tree top, which is reported as unavailable rather than
	// filled with a directory the work does not live in.
	root := any(unavailable("anime_root_unavailable"))
	treeTop, placed, err := workTreeTop(ctx, tx, c.material.management.managedID)
	if err != nil {
		return acquisitionIntent{}, err
	}
	if placed {
		root = frozenValue("known", treeTop)
	}
	var mapping any = unavailable("path_mapping_unconfigured")
	if connection != "" {
		var raw []byte
		if err := tx.QueryRowContext(ctx, `SELECT config FROM connections WHERE id=$1 AND kind='qbittorrent' AND state='configured'`, connection).Scan(&raw); err != nil {
			return acquisitionIntent{}, err
		}
		var config struct {
			SaveRoot       string `json:"save_root"`
			DownloaderRoot string `json:"downloader_root"`
		}
		if err := json.Unmarshal(raw, &config); err != nil {
			return acquisitionIntent{}, err
		}
		paths, err := qbittorrent.DerivePathSnapshot(qbittorrent.RuntimeConfig{SaveRoot: config.SaveRoot, ApplicationRoot: config.DownloaderRoot})
		if err != nil {
			return acquisitionIntent{}, err
		}
		if paths.RemoteRoot != "" && paths.ApplicationRoot != "" {
			mapping = frozenValue("known", map[string]any{"remote_root": paths.RemoteRoot, "application_root": paths.ApplicationRoot, "semantics_version": "qb-path-mapping-v1"})
		}
	}
	var effect anime.FileEffectRules
	if err := json.Unmarshal(captureSections(c.material).Sections[anime.SectionFileEffect], &effect); err != nil {
		return acquisitionIntent{}, err
	}
	collision := map[anime.CollisionPolicy]string{anime.CollisionPolicyFail: "block", anime.CollisionPolicyVersioned: "version", anime.CollisionPolicyOverwrite: "overwrite"}[effect.CollisionPolicy]
	replacements := []any{}
	intent := "ensure_present"
	if collision == "overwrite" && overwriteTargetAbsent && len(captures) == 0 {
		// Overwrite only authorizes replacing an existing file. The caller
		// observed no overwrite target present, so the delivery is an
		// ordinary ensure_present that refuses (block) anything appearing at a
		// path before publication; no replacement approval is involved.
		collision = "block"
	}
	if collision == "overwrite" && len(captures) > 0 {
		// One approval per present managed overwrite target (a batch replaces
		// per file). A target observed absent needs none: the file plan
		// downgrades its operation to an ordinary ensure_present. Without any
		// approval the delivery stays ensure_present/overwrite: it replaces what
		// it finds, and a managed file's placement is displaced when the new
		// placement is admitted.
		ordered := append([]*ReplacementCapture(nil), captures...)
		for _, capture := range ordered {
			if capture == nil {
				return acquisitionIntent{}, existingUnsupported()
			}
		}
		// Path order: the frozen list is sorted by target path, and rechecks
		// take their share locks in one order across acceptances.
		sort.Slice(ordered, func(i, j int) bool { return ordered[i].path < ordered[j].path })
		for i, capture := range ordered {
			if i > 0 && ordered[i-1].path == capture.path {
				return acquisitionIntent{}, existingUnsupported()
			}
			approved, err := capture.recheck(ctx, tx, c.material.release.id, c.material.management.managedID)
			if err != nil {
				return acquisitionIntent{}, err
			}
			replacements = append(replacements, json.RawMessage(protocol.Canonical(approved)))
		}
		intent = "replace"
	}
	cleanup := "none"
	if effect.Operation == anime.FileEffectRulesOperationMove {
		cleanup = "remove_source_after_publish"
	}
	naming, err := projectNamingSection(captureSections(c.material).Sections[anime.SectionNamingRules])
	if err != nil {
		return acquisitionIntent{}, err
	}
	delivery := map[string]any{"operation": effect.Operation, "intent": intent, "collision": collision, "route": map[string]any{"mode": "unclassified", "root": root, "path_mapping": mapping}, "naming": map[string]any{"schema_version": "naming-rules-v1", "templates": json.RawMessage(protocol.Canonical(naming)), "vars_choice": "frozen", "runtime_version": "naming-v1"}, "source_cleanup": map[string]any{"mode": cleanup}, "replacements": replacements}
	var files []int64
	if seed, err := admissionSeed(c); err == nil && seed != nil {
		for _, file := range seed.Manifest.Files {
			files = append(files, file.SizeBytes)
		}
	}
	timeouts, err := readDownloadTimeouts(ctx, tx)
	if err != nil {
		return acquisitionIntent{}, err
	}
	limits, err := productionLimits(files, timeouts)
	if err != nil {
		return acquisitionIntent{}, err
	}
	mv, err := valueOf(media)
	if err != nil {
		return acquisitionIntent{}, err
	}
	dv, err := valueOf(delivery)
	if err != nil {
		return acquisitionIntent{}, err
	}
	if err := protocol.Validate("DeliveryV1", dv); err != nil {
		return acquisitionIntent{}, err
	}
	return acquisitionIntent{entryMode: mode, title: title, media: mv, delivery: dv, limits: limits}, nil
}

// fileStagePasses is how many times one file's bytes cross storage during a
// file_execute attempt: the copy read and write. Verification is path + size
// (stat only), and rename/hardlink publication moves no bytes.
const fileStagePasses = 2

// planPerFile is the planning cost of one release file (capture, naming,
// target probe, DB rows). Plan satisfaction stats targets and never reads
// content, so the plan budget does not scale with bytes.
const planPerFile = 2 * time.Second

// Fixed policy V1 values are baked in. Runtime-owned attempt limits and timeouts are read from the
// canonical KindPolicy registry, never from a client or test golden vector.
// Storage stages scale their per-attempt timeout with the accepted inventory
// (fileSizes, one entry per release file): a progressing slow copy must not be
// cut off by a fixed wall clock.
//
// The two download timeouts come from the Global download_timeouts setting
// read in the accepting transaction (readDownloadTimeouts) and are frozen here
// with the derived sync.max_polls; the download task never reads the setting.
func productionLimits(fileSizes []int64, timeouts anime.DownloadTimeouts) (protocol.Value, error) {
	if err := timeouts.Validate(); err != nil {
		return protocol.Value{}, fmt.Errorf("production limits: %w", err)
	}
	var largest, total int64
	for _, size := range fileSizes {
		if size > largest {
			largest = size
		}
		if size > 0 {
			total += size
		}
	}
	policies := map[string]worker.KindPolicy{}
	for _, p := range worker.DefaultPolicies() {
		policies[p.Kind] = p
	}
	// Invariant: business attempts never exceed the claim budget the SQL
	// work-creation paths freeze for inspect/plan (see workitems claim consts).
	for kind, claimMax := range map[string]int{workitems.KindArtifactInspect: workitems.ArtifactInspectClaimMaxAttempts, workitems.KindPostProcessPlan: workitems.PostProcessPlanClaimMaxAttempts} {
		if p, ok := policies[kind]; !ok || p.MaxAttempts > claimMax {
			return protocol.Value{}, fmt.Errorf("production limits: %s business max_attempts exceeds claim budget %d", kind, claimMax)
		}
	}
	stageTimeout := func(kind string, timeout time.Duration) any {
		p := policies[kind]
		return map[string]any{"max_attempts": p.MaxAttempts, "attempt_timeout_seconds": int64(timeout.Seconds()), "wall_seconds": 86400}
	}
	stage := func(kind string) any { return stageTimeout(kind, policies[kind].BusinessAttemptTimeout()) }
	// One file operation moves one file, so its budget covers the largest one.
	fileTimeout := worker.ScaleStorageAttempt(policies[workitems.KindFileExecute].BusinessAttemptTimeout(), largest, fileStagePasses)
	// One inspect attempt hashes the whole batch once and re-reads the primary
	// (largest) file after the settle window.
	inspectTimeout := worker.ScaleStorageAttempt(policies[workitems.KindArtifactInspect].BusinessAttemptTimeout(), total+largest, 1)
	planTimeout := worker.ScaleStorageAttempt(policies[workitems.KindPostProcessPlan].BusinessAttemptTimeout()+time.Duration(len(fileSizes))*planPerFile, 0, 1)
	sync := policies[workitems.KindQBSync]
	submit := policies[workitems.KindQBSubmit]
	// max_polls is fixed at acceptance: ceil(download_wait / interval) + 1
	// (LimitsV1 and the download_tasks budget shape both check it).
	const pollInterval = int64(60)
	maxPolls := (timeouts.DownloadWaitSeconds+pollInterval-1)/pollInterval + 1
	limits := map[string]any{"policy_version": 1, "prepare_timeout_seconds": 60, "download_wait_seconds": timeouts.DownloadWaitSeconds, "stall_seconds": timeouts.StallSeconds, "submit": stage(workitems.KindQBSubmit), "sync": map[string]any{"poll_interval_seconds": pollInterval, "attempt_timeout_seconds": int64(sync.Deadline.Seconds()), "max_consecutive_errors": 5, "max_total_errors": 100, "max_polls": maxPolls}, "inspect": stageTimeout(workitems.KindArtifactInspect, inspectTimeout), "plan": stageTimeout(workitems.KindPostProcessPlan, planTimeout), "file": stageTimeout(workitems.KindFileExecute, fileTimeout), "reconcile": map[string]any{"wall_seconds": 86400, "attempt_timeout_seconds": 60, "max_lookups": 100}, "settlement": map[string]any{"max_errors": 5, "attempt_timeout_seconds": 120, "lease_seconds": 60, "heartbeat_seconds": 15}, "handoff": map[string]any{"max_attempts": submit.MaxAttempts, "attempt_timeout_seconds": 120, "wall_seconds": 86400}, "retention": map[string]any{"detail_seconds": 2592000, "receipt_after_accept_seconds": 2592000, "receipt_after_terminal_seconds": 2592000}}
	v, err := valueOf(limits)
	if err == nil {
		err = protocol.Validate("LimitsV1", v)
	}
	if err != nil {
		return v, fmt.Errorf("production limits: %w", err)
	}
	return v, nil
}

// readDownloadTimeouts reads the Global download_timeouts setting in the
// accepting transaction; no saved row means the code default. A stored value
// that does not validate fails the acceptance closed rather than freezing a
// substitute.
func readDownloadTimeouts(ctx context.Context, tx *sql.Tx) (anime.DownloadTimeouts, error) {
	var schema string
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT schema_version, value FROM global_config_sections WHERE section_name = $1`, anime.SectionDownloadTimeouts).Scan(&schema, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return anime.DefaultDownloadTimeouts(), nil
	}
	if err != nil {
		return anime.DownloadTimeouts{}, fmt.Errorf("read download timeouts: %w", err)
	}
	if schema != anime.DownloadTimeoutsSchemaVersion {
		return anime.DownloadTimeouts{}, fmt.Errorf("download timeouts: unsupported schema version %q", schema)
	}
	value, err := anime.DecodeDownloadTimeouts(raw)
	if err != nil {
		return anime.DownloadTimeouts{}, fmt.Errorf("download timeouts: %w", err)
	}
	return value, nil
}
