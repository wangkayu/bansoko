# Bansoko agent guide

Bansoko subscribes to anime RSS feeds, downloads releases through qBittorrent, verifies them, and places the files in a media library. This guide is for an external agent or workflow that operates a running instance. It is embedded in the server binary, so it always matches the version you talk to.

Read sections 1 to 4 once. Section 5 is the task index. Section 7 lists errors and exit codes.

## 1. Ways to operate the instance

| Surface | Use it for |
|---|---|
| MCP server at `/api/v1/mcp` (streamable HTTP, server name `bansoko`) | Preferred. Tools are generated from the HTTP contract. |
| HTTP API under `/api/v1`, described by `api/openapi.yaml` | Anything MCP does not cover (see "Not in MCP"). Operation names below are the OpenAPI `operationId` values. |
| `bansoko` command line | Scripts and the steps MCP does not cover. |

Authentication is always `Authorization: Bearer <token>`. Use a long-lived API token (created by the owner under Settings, API tokens, or with `bansoko api-token create`). A session from `POST /api/v1/auth/login` also works but expires: the browser gets it as an HttpOnly cookie, and a non-browser client asks for `"session_transport": "token"` in the login body to receive it as `session_token` and send it as a bearer. The MCP server never uses a credential of its own: every tool call replays as an HTTP request with your bearer token, so a tool can do exactly what your token can do over HTTP, and nothing more.

An API token carries a fixed set of **scopes**, chosen when it is created and kept on rotation:

| Scope | Allows |
|---|---|
| `read` | Every read, including read-only previews sent as POST. Always part of the set. |
| `write` | Every operation that changes state or starts work (MCP tools marked as writes). |
| `secrets` | Reading fields marked secret (feed locators, seed and release locations, item keys) as stored. Without it each such value reads as the placeholder `[secret]`. |

New tokens get `read` and `write`. An agent token should not carry `secrets`. A call outside the token's scopes fails with HTTP 403 `TOKEN_SCOPE_INSUFFICIENT`; `required_scope` names the missing scope. A session token holds every scope. A token can never create, rotate or reveal a token with a scope it does not hold itself.

Register the MCP server in a client:

```
claude mcp add bansoko --transport http http://HOST:PORT/api/v1/mcp --header "Authorization: Bearer TOKEN"
```

Use `bansoko` with `--endpoint http://HOST:PORT --token TOKEN` (or `bansoko login`). Add the global `--json` flag and stdout carries exactly one JSON document: the result, or on failure `{"error":{"code","message","http_status","exit_code"}}`. Human text goes to stderr.

This guide is available as the MCP resource `bansoko://agent-guide`, the MCP prompt `agent_guide`, `GET /api/v1/agent-guide` (`?format=skill` returns it as a SKILL.md), and `bansoko guide` (`bansoko guide --skill`).

## 2. Concepts

- **Managed anime**: one subscribed show or movie instance. Its id is `managed_anime_id`. It owns the metadata, subgroup selection, rules, RSS sources and the whole pipeline below.
- **Subscription**: the state of a managed anime: `pending` (not started; configuration is editable and nothing runs, manual commands included), `active` runs automatic work, `stopped` only accepts manual commands. A new subscription starts `pending`. Deleting a subscription retires the anime; local history and external data are kept.
- **Candidate**: a saved, editable plan (an *AnimeDraft*) that is not running yet. Every save creates an immutable **revision**. Two numbers matter and must never be mixed up:
  - `current_revision` is the exact saved content. Previews, activation and saved-revision one-shots name it as `revision`.
  - `version` is the candidate's compare-and-swap token. Mutations send it as `expected_version`.
- **Activation**: turns one exact candidate revision into a managed anime with its subscription, sources and metadata in one transaction.
- **Release**: one RSS item seen by an RSS source. It gets a verdict (accepted, filtered, held, ...). Accepted releases become download tasks.
- **Pipeline** (the *owner chain*): RSS source, release, download task, artifact (verified downloaded files), file operation (placement into the library). Each stage has a `state`; failed stages carry a reason.
- **Metadata hold**: while an anime's required BGM/TMDB bindings are missing, its releases are held instead of downloaded. `get_workspace` shows `metadata_hold`, whose `missing` names the bindings (`bgm`, `tmdb`) to add. A held release's log record carries the same set as `details.metadata_missing`.
- **Execution record**: the history of one accepted acquisition (automatic, subscription add, or one-shot) with fixed stages and outcomes.
- **One-shot**: a finite download of one chosen torrent that is not a subscription. The server uses a temporary owner graph and reclaims it when done. You track it by `one_shot_id`.
- **File manager**: lists library files and can release a managed video and match a detached video (one without an owner) to a season and episode.
- **Notifications** (plugin targets and deliveries) are configured by the owner in the web UI and HTTP API. No MCP tool or CLI command covers them.

There is no "needs attention" inbox. Problems are visible as failed states on the pipeline and in execution records. Repair means asking the system to run the failed step again (section 6), never editing data.

## 3. Safety rules

1. **Confirm before destroying.** `delete_candidate` and `delete_subscription` are irreversible. Ask the human first, and say what will be removed. Prefer `patch_subscription` with `state=stopped` when the human only wants a pause.
2. **Never reveal secrets.** Do not request, log or repeat download URLs, torrent URLs, RSS locators with embedded keys (passkeys), or API tokens. No MCP tool returns a download, feed or torrent URL: the fields that carry one (feed snapshots, seed and release locations, one-shot readbacks, release identity keys) are withheld, and any other URL with a query string or credentials is replaced with `[withheld]` in every tool result. RSS source read views show a redacted `locator_display` of the configured feed; seeds and releases show no location at all. Address releases by `release_id`, never by an item key. Candidate drafts carry each RSS source's `locator`; without the `secrets` scope it reads as `[secret]`. To edit such a candidate, send the draft back with `[secret]` left in place: `patch_candidate` keeps the stored locator of the same source (same `source_kind`, and the same `locator_display` when you send one). Any other draft write that still holds `[secret]`, or a placeholder that matches no single stored source, fails with `DRAFT_INVALID` and detail code `SECRET_PLACEHOLDER_UNRESOLVED`: ask the human for the real feed address instead of guessing. If your token does hold `secrets`, treat candidate payloads as secret and do not paste them into messages for other people.
3. **Read before you write.** Every owner mutation needs the current `version` (section 4). Read the owner first; never guess or reuse an old number.
4. **Do not invent identifiers.** Use ids returned by earlier calls. Do not fabricate a `revision`, `release_id` or `rss_source_id`.
5. **No manual data edits.** Do not touch the database or the library directly to repair a problem. Use the retry tools.
6. **Respect maintenance.** While the instance is in maintenance every write fails with `WRITES_REJECTED` (HTTP 503) and reads still work. Wait and retry later; do not loop.
7. **Overwrite deletes files.** A `file_effect` with `collision_policy=overwrite` replaces an existing file at the target path and permanently deletes it, including a file the library manages: that file's placement becomes `displaced` and its episode is no longer counted for its anime (another instance's or another episode alike). Alignment (moving files to their expected names) follows the same setting and treats a video and its subtitles as one unit. Do not set `overwrite` in a draft unless the human asked for it.
8. **Do not poll hot.** `poll_workspace` already waits (default 30 seconds, at most 120). `bansoko wait` polls every 2 seconds by default. Do not call read tools in a tight loop.

## 4. Versions, idempotency and retries

**Compare-and-swap.** Owner mutations carry the version you read. A wrong version fails with HTTP 409 `OWNER_VERSION_CONFLICT` and `current_version`. Re-read the owner, re-apply your intent, then retry once. Do not blindly resend the same number. In MCP the number is the `expected_version` argument (it becomes the HTTP `If-Match` header or a body field, depending on the operation).

**Idempotency keys.** Operations that start work take an `Idempotency-Key` (8 to 128 characters). In MCP the key is generated for you on every call unless you pass `idempotency_key`; `activate_candidate` and `duplicate_subscription` require you to supply one. The server replays the first result for a repeated key with the same request, and rejects the same key with a different request as `IDEMPOTENCY_KEY_REUSED` (HTTP 409).
- To retry a write safely after a timeout or a lost response, repeat the call **with the same explicit `idempotency_key`**. A call with a fresh key is a new request.
- A repeated key replays for at least 30 days. After that the server may have dropped the record, and the same key can run as a new request.
- Reads and `poll_workspace` are always safe to repeat.
- `create_candidate` and `patch_candidate` are not idempotent: each success inserts a row or advances the version.

**Failure handling.**

| Result | Meaning | What to do |
|---|---|---|
| HTTP 400, `REQUEST_INVALID` | Malformed body or unknown field (bodies are strict) | Fix the arguments |
| HTTP 401 | Missing or invalid bearer token | Stop; ask the human for a token |
| HTTP 403, `TOKEN_SCOPE_INSUFFICIENT` | The token lacks the scope in `required_scope` | Stop; do not retry. Tell the human which scope the step needs |
| HTTP 404, `NOT_FOUND` | The owner does not exist (any more) | Re-list; do not retry |
| HTTP 409 | Version conflict or a precondition that is not true now | Re-read, then decide |
| HTTP 422 | The request is well formed but refused (validation or business rule) | Read `code` and `details`; change the request |
| HTTP 503 | Maintenance or a dependency is down | Back off, retry later |
| HTTP 5xx | Server fault | Retry reads; retry writes only with the same `idempotency_key` |

In MCP a failed call returns an error result whose structured content is `{"status", "code", "message", ...}` (the HTTP error body plus the status). Success returns the HTTP response body as structured content; operations with an empty answer return `{"ok": true}` or, for deletes, `{"deleted": true, ...}`.

## 5. Task recipes

Columns: the MCP tool, the HTTP `operationId`, and the `bansoko` command. A dash means that surface has no equivalent. Every tool argument is the HTTP parameter or body field of the same name (`expected_version` replaces the `If-Match` header, `idempotency_key` the `Idempotency-Key` header, and the search keyword is `query`).

### 5.1 Subscription (group `subscription`)

Look around:

| Step | MCP tool | operationId | CLI |
|---|---|---|---|
| List subscribed anime with state, metadata hold, RSS health, progress | `get_subscriptions` | `listAnime` | `bansoko list` |
| Read one anime's whole pipeline | `get_workspace` | `getAnimeWorkspace` | `bansoko status` |
| Wait until the pipeline settles (stable = every task completed or failed) | `poll_workspace` | `getAnimeWorkspace` | `bansoko wait` |
| Search a provider catalog for programs (AniBT and Mikan) | `search_anime` | `searchProviderCatalog` | `bansoko search` |
| Search Bangumi or TMDB for a metadata binding | `search_metadata` | `searchMetadata` | `bansoko metadata search` |
| Parse a release title | `parse_title` | `parseReleaseTitle` | `bansoko parse-title` |
| Parse a torrent's file list | `parse_torrent` | `parseTorrentFiles` | `bansoko parse-torrent` |
| List the feed sites with a parsing adapter (a custom feed on another host is parsed without guarantee) | `list_feed_sites` | `listFeedSites` | - |

Subscribe to a new show:

| Step | MCP tool | operationId | CLI |
|---|---|---|---|
| 1a. Discover the release groups of a provider show (`provider` and `provider_entry_ref`; provider shows only) | `discover_feed_groups` | `discoverProviderEntryGroups` | `bansoko feed groups` |
| 1b. Fetch a group's feed snapshot now (returns `snapshot_id`, `snapshot_version`, `content_hash` and the seeds) | `refresh_feed_snapshot` | `previewProviderEntryRSS` | `bansoko feed refresh` |
| 1c. Read the latest stored snapshot without fetching (`freshness` tells whether to refresh) | `get_feed_snapshot` | `readProviderEntryRSS` | `bansoko feed snapshot` |
| 2. Load the Global business configuration into your draft (returns the draft with `business` and `business_digest`; writes nothing) | `load_business_preset` | `loadBusinessPresetIntoDraft` | - |
| 3. Save the loaded draft as a candidate (new row, `version` and `current_revision` returned) | `create_candidate` | `saveAnimeConfigCandidate` | `bansoko candidate create` |
| 3b. Change the plan (full draft replacement, needs `expected_version`; load the business again first) | `patch_candidate` | `patchAnimeConfigCandidate` | `bansoko candidate patch` |
| 4. Preview what would be accepted, group by group, without writing | `rss_list_view` | `readAnimeRSSListView` | `bansoko candidate preview`, `bansoko preview` |
| 5. Activate the exact revision (pass `revision`, `expected_version`, your own `idempotency_key`; add `start: true` when the configuration is complete, otherwise it is created `pending`) | `activate_candidate` | `subscribeAnimeConfigCandidate` | `bansoko candidate activate [--start]` |
| 5a. Optional, while `pending`: only take releases published after the subscription was created (`enabled`, `expected_version` from `get_workspace`) | `set_new_releases_only` | `setAnimeNewReleasesOnly` | - |
| 5b. Start a `pending` subscription (`state: active`, `expected_version` from `get_workspace`) | `patch_subscription` | `patchAnimeSubscription` | - |
| 6. Fetch the feed now (ids are in the activation result `rss_sources`) | `refresh_rss` | `refreshRSSSource` | `bansoko refresh` |
| 7. Watch it settle | `poll_workspace` | `getAnimeWorkspace` | `bansoko wait` |

Notes:
- Read candidates with `list_candidates` and `get_candidate` (`listAnimeConfigCandidates`, `getAnimeConfigCandidate`; `bansoko candidate list`, `bansoko candidate get`). Remove an unneeded one with `delete_candidate` (`deleteAnimeConfigCandidate`; `bansoko candidate delete`), after confirmation.
- A **draft** is the full object shown in the appendix. For a custom feed use `origin: custom` with one `custom_rss` source and an `instance_label`. For AniBT or Mikan shows use that origin with `origin_entry_ref` and no `rss_sources`; the subgroup selection supplies the sources. Drafts with `origin: oneshot` cannot be subscribed (`ORIGIN_NOT_SUBSCRIBABLE`).
- Feed sites: `list_feed_sites` returns the declared adapters (`id`, `name`, `hosts`, `match`, `identity`). A custom URL on one of the hosts of a `host` entry, or a subdomain of one, is parsed by that site's adapter. A `content` entry (UNIT3D) has no hosts: on any other host, a feed that carries that architecture's signature is parsed by its adapter. Everything else is parsed as plain RSS/Atom and is not guaranteed. Every item needs a title and an HTTP(S) `.torrent` download; its infohash comes from the feed (`identity` `feed`) or from the torrent at expansion (`expansion`). Magnet links are not supported: an item whose only download is a magnet is skipped. A feed whose every item is skipped fails with `FEED_NO_USABLE_ITEMS`, and a listed site or detected architecture whose feed no longer has its expected shape fails with `FEED_SITE_FORMAT_CHANGED`; neither is fixed by retrying with another adapter.
- For a provider draft, build `subgroups.entries` from the groups `discover_feed_groups` returned: one entry per group with `provider`, `group_ref`, `display_name`, `selection_mode` (`manual` to pin your choice), `manual_selected`, `effective_selected`, `score` (1 to 100) and `count_in_limit`.
- **Every draft needs its business snapshot.** Preview, save and activation use only the configuration the draft carries; the server never fills in the current Global for a missing one (`rss_list_view` refuses with `BUSINESS_SNAPSHOT_REQUIRED`, and an anime activated from a draft without one can never download). Load it for one-shot drafts too, so the admission runs the configuration you previewed. Pass your draft to `load_business_preset` and use the draft it returns. Your `subgroups.entries` win over the Global ones, a rules section you send (`{"mode": "replace", "value": ...}`) keeps your value, and a rules section you leave out gets a copy of the Global value. Every section is one-time input: the draft and, after activation, the subscription keep their own copy, and later Global changes never reach them. To start over from the current Global values, drop the sections and load again. Do not edit `business` or `business_digest` by hand (`BUSINESS_SNAPSHOT_INVALID`): change the draft and load again. `get_business_preset` (`getBusinessPreset`) shows the Global business on its own.
- Feed snapshot tools never return a download or feed URL. A seed is identified by its snapshot identity (`snapshot_id`, `snapshot_version`, `content_hash`) plus its `stable_seed_key`.
- `rss_list_view` modes: `current_input` (an unsaved `draft`), `candidate_revision` (`candidate_id` and `revision`), `owner` (`managed_anime_id`, a running anime). For a pending (not started) provider subscription, `owner` is the test bench instead: its configuration over the `feed` identities you pass, like `current_input`, with every download blocked as `not_started`. `owner_draft` is UI-only and is refused. For provider drafts pass each group's snapshot identity in `feed` (`provider`, `provider_entry_ref`, `provider_group_ref`, `snapshot_id`, `snapshot_version`, `content_hash`). A provider group without one is reported as `snapshot_required`: fetch the snapshot first (step 1b).
- Start, stop or restart automatic work: `patch_subscription` (`patchAnimeSubscription`) with `state` of `active` or `stopped`. `active` on a `pending` subscription is its first start (the first refresh discovers and evaluates the feed); `active` on a `stopped` one resumes it (the first refresh only records a baseline). A `pending` subscription cannot be stopped (`SUBSCRIPTION_NOT_STARTED`).
- New releases only: set `new_releases_only: true` in the draft, or call `set_new_releases_only` while the subscription is `pending`. The baseline is the moment the subscription was created (`start_baseline_at`), not its start; releases published while pending count as new. A release published before it, or without a publish time, gets the verdict `start_baseline` and is not downloaded or re-evaluated when rules change. To download one anyway, call `retry_release` on it: that applies every rule as usual. The option is frozen once the subscription has started (`NEW_RELEASES_ONLY_LOCKED`); to change it, delete the subscription and subscribe again.

Maintain a running anime (each needs the owner `expected_version` from `get_workspace`):

| Step | MCP tool | operationId | CLI |
|---|---|---|---|
| Replace metadata bindings (BGM, TMDB, title override, total episodes, year, season) | `patch_anime_metadata` | `patchAnimeMetadata` | - |
| Re-run metadata matching for an active subscription (useful when an anime is held waiting for metadata). It follows the same fill-if-empty rule as the scheduled `metadata_match`: BGM, the TMDB tuple, `total_episodes`, `year` and `season` are filled only when empty, never overwritten, and a partial result such as BGM alone is saved | `match_metadata` | `matchAnimeMetadata` | - |
| Re-pull the bound work from its provider and refresh the stored titles (BGM, TMDB) for an active subscription. It bypasses lookup caches: naming templates that read `bgm.ep_title` or `tmdb.ep_title` re-resolve from fresh provider data on the next render, and pending file alignment previews show the refreshed titles | `refresh_metadata` | `refreshAnimeMetadata` | - |
| Replace rule sections (parser, release filter, file filter, naming, file effect) | `patch_anime_rules` | `patchAnimeRules` | - |
| Discover new subgroups and reapply the selection policy | `discover_subgroups` | `discoverAnimeSubgroups` | - |
| Re-evaluate existing releases against current rules | `reevaluate_releases` | `reevaluateAnimeReleases` | - |
| Copy the subscription into a new `pending` one with its own `instance_label` (current saved configuration including file-maintenance settings; no state, history, files or managed file lists; overlap with the source is not checked; your own `idempotency_key`) | `duplicate_subscription` | `duplicateAnimeSubscription` | - |
| Retire the subscription (destructive, needs confirmation) | `delete_subscription` | `deleteAnimeSubscription` | - |

### 5.2 One-shot download (group `one_shot`)

A one-shot downloads one chosen torrent without creating a subscription.

| Step | MCP tool | operationId | CLI |
|---|---|---|---|
| 1. Fetch the feed snapshot of the seed's group and pick a seed from it | `refresh_feed_snapshot` | `previewProviderEntryRSS` | `bansoko feed refresh` |
| 2. Expand the chosen seed so its files and targets exist to review (downloads nothing) | `expand_seed` | `expandSnapshotSeed` | - |
| 3. Load the Global business into the draft (section 5.1), then preview it (`current_input`) | `rss_list_view` | `readAnimeRSSListView` | `bansoko preview` |
| 4. Admit the seed | `acquire_seed_once` | `acquireSeedOnce` | `bansoko one-shot acquire` |
| 5. Admit one seed straight from a persisted snapshot with the reviewed draft | `download_snapshot_seed` | `downloadSnapshotSeed` | - |
| 6. Follow it (stop at the terminal outcome, or `NOT_FOUND` after purge) | `get_one_shot_acquisition` | `getOneShotAcquisition` | `bansoko one-shot get` |
| 7. See the attempt history | `list_one_shot_attempts` | `listOneShotExecutionAttempts` | `bansoko one-shot attempts` |

Notes:
- `rss_list_view` renders a seed's files only after it was expanded. A seed the server has not expanded is `pending`: it carries the RSS facts and a `download_action` blocked with `seed_not_expanded`, and no file list. `expand_seed` fetches that seed's torrent once, verifies it against the identity the snapshot published, caches it and returns the manifest (or `state=unavailable` with `torrent_fetch_failed`, `torrent_parse_failed` or `torrent_missing`). Expansion downloads nothing; a failed expansion is cached nowhere, so the row is pending again and retrying is simply calling `expand_seed` again. `rss_list_view` alone never fetches a torrent.
- Each expanded file carries its `rendered_target`. A naming value that exists but is empty (an unset year, a missing tag) renders empty and the path is tidied; `empty_facts` lists those values. A file fails naming when the result is not a valid path (an empty name or folder): `reason_code` is `naming_fact_missing_<first value>` and `empty_facts` lists every value that was empty. A template that reads a variable whose source does not exist here (`bgm.*` without a Bangumi binding, `tmdb.*` without a TMDB binding, `anibt.*` on a non-AniBT seed) fails with `naming_fact_undefined_<variable>`, and `empty_facts` lists those variables. Files of one seed that would land on one target are settled like this: two copies of one episode, or two subtitles of one video, keep the first by path and the rest are `target_duplicate`; any other group (different episodes, subtitles of different videos) is refused as a whole with `target_duplicate`. A subtitle lands beside its video as `<video name>.<suffix>.<ext>`; the default suffix is the subtitle language tag (`sub.lang_tag`), read from the subtitle file name first and from the release title otherwise. Fill in the value, bind the source, or give the template a default, then preview again.
- `anime.year` is the `year` you set on the subscription metadata (draft, subscription or one-shot), 1900 to 2100 or `null`. `match_metadata` and the scheduled `metadata_match` automation fill it from the matched TMDB work only when it is empty and never change a stored year; nothing else fills it. The metadata PATCH replaces it, so send the current `year` back on every PATCH or it is cleared.
- `anime.season`, `anime.season_folder`, `anime.season_path` and `ep.s00e00` read the `season` you set on the subscription metadata (draft, subscription or one-shot): a whole number from 0 (specials) or `null`. `null` means no season folder and an episode-only `E07`. It is separate from the TMDB binding's `season`, which only selects the TMDB season for episode lookups; changing one never changes the other. `match_metadata` and the scheduled `metadata_match` fill an empty `season` from the TV season of the TMDB binding they add, and never change a stored one. Like `year`, send the current `season` back on every metadata PATCH or it is cleared.
- A TMDB binding can be set by id without searching: send `tmdb` with `work_id`, `media_kind` and the TMDB `season` (0 for a movie). The id is saved as given and is not checked against TMDB.
- `release_format` (`tv`, `movie`, `ova` or `ona`) on catalog items and anime list rows is a read-only fact from the provider catalog: Mikan's homepage section (days 0 to 6 are `tv`, section 7 is `movie`, section 8 is `ova`) or AniBT's format. It is absent when the provider states none and for custom subscriptions. It is not part of the anime metadata, cannot be edited and is not filled by `match_metadata`. The subscribed wall and the catalog browse rows use it for the movie and OVA/ONA rows. A Bangumi metadata search result shows the subject platform as a display-only `release_format`. Naming does not read it.
- `download_snapshot_seed` takes the same identity reference as `expand_seed` plus `input_source` and the draft or candidate revision, and admits that one seed. Use it when the seed came from a snapshot you already hold; use `acquire_seed_once` when the seed reference is part of a wider one-shot input. It returns the same one-shot handle.
- `acquire_seed_once` takes either `input_source=saved_revision` (`candidate_id`, `revision`) or `input_source=current_input` (a full `draft`), plus `selected_seed` and an optional `episode_offset`.
- The `selected_seed` is a **reference** to one seed of the snapshot from step 1. Send `provider`, `provider_entry_ref`, `provider_group_ref` (the snapshot's `group_ref`), `snapshot_id`, `snapshot_version`, `snapshot_content_hash` (the snapshot's `content_hash`) and the seed's `stable_seed_key`, and no `locator`. The server reads that exact snapshot and fills in the torrent URL and the seed facts itself; the result is frozen exactly as if the full seed had been sent. You may also send the seed's `title`, `published_at`, `size_bytes` or `info_hash`; each must equal the snapshot. Example: `{"provider": "mikan", "provider_entry_ref": "3001", "provider_group_ref": "678", "snapshot_id": "...", "snapshot_version": 4, "snapshot_content_hash": "sha256:...", "stable_seed_key": "..."}`.
- A reference to a snapshot that expired, was replaced, or does not contain the key fails with `SEED_PREVIEW_STALE` (HTTP 422) and writes nothing: refresh the snapshot and choose again. An incomplete reference is `SEED_INVALID`. The seed's identity is the `info_hash` its feed published, else the infohash of its expanded torrent; admission freezes it. A feed infohash the expanded torrent contradicts fails with `SEED_IDENTITY_MISMATCH`; an expanded torrent whose infohash cannot be computed fails with `SEED_IDENTITY_REQUIRED`.
- Retrying with the same `idempotency_key` and the same reference returns the original one-shot, even after the snapshot expired.
- The HTTP and CLI callers may still send the full seed with its `locator` (the frontend does). `get_one_shot_acquisition` never returns the locator: its `input_snapshot` shows the seed without it.
- Caller binding: an agent request needs a complete `origin: oneshot` draft and an explicit authorization snapshot. A one-shot names its seed directly and is never bound by group selection; the seed's exact persisted snapshot proves it exists. A human request admits a seed of a provider draft's (`anibt` or `mikan`) own entry (`origin_entry_ref` equals the seed's `provider_entry_ref`) from any of its groups. A human custom draft admits a custom seed: under `input_source=saved_revision` the server derives the source/group from the revision and any other seed is `SEED_PREVIEW_STALE`; under `input_source=current_input` (the one-shot's own unsaved configuration) the seed's persisted custom snapshot is the binding.
- `rss_list_view` with `one_shot: true` (current_input only) is the one-shot browse: every configured and observed group lists its supplied snapshot whatever the draft selects, every group previews with `episode_offset`, and a custom draft binds the custom snapshot it names. Use it to preview exactly what `download_snapshot_seed` with that draft and offset will create.
- Use `get_one_shot_acquisition`, not `get_workspace`, for a one-shot.
- A one-shot whose import (file placement) failed can retry that stage from its execution record (`retry_import`, section 5.4). Any other failed one-shot is final: there is no replan, so to download the seed again admit it again as a new one-shot (steps 1 to 5) with a new `idempotency_key`.

### 5.3 File management (group `files`)

Match a detached video in the media library to a season and episode of an anime. You supply the anime, season and episode only; the server renders the target path with the anime's naming template and never overwrites a file. A managed video is refused: its episode follows the subscription's parse result. `getFileManagerEntry` shows the frozen `episode` the acquisition recorded, which explains why a file was not recognized.

| Step | MCP tool | operationId | CLI |
|---|---|---|---|
| Release a managed video so it can be matched (the file stays on disk; its bound subtitles are released with it) | `release_file_entry` | `releaseFileManagerEntry` | - |
| List the TMDB seasons of the anime's TMDB binding | `list_tmdb_seasons` | `listAnimeTMDBSeasons` | - |
| List the episodes of one season | `list_tmdb_season_episodes` | `listAnimeTMDBSeasonEpisodes` | - |
| Preview the match (writes nothing; returns `revision` and `target_path`) | `preview_file_match` | `previewFileManagerMatch` | - |
| Apply it, passing back `expected_revision`, `expected_target` and `conflict` (`block` or `version`) | `match_file` | `matchFileManagerEntry` | - |

**Missing or changed files.** A scan that no longer finds a managed file (`missing`) or finds other content at its path (`fingerprint_conflict`) records an observation only: the placement stays active and recovers by itself when the file comes back. Nothing is redownloaded or released automatically. Two manual, per-file exits exist; use one only when the human asks:

| Step | MCP tool | operationId | CLI |
|---|---|---|---|
| List an anime's missing or changed files (also in the file-maintenance overview as `placement_issues`) | `list_placement_issues` | `listPlacementIssues` | - |
| Download the episode again: the placement's release runs through normal acceptance; when the new file is placed the old placement becomes `displaced` (cause `redownload`). Filtered, held and present are results, and the request is then withdrawn | `redownload_placement` | `redownloadPlacement` | - |
| Stop managing the file: it stays on disk and the episode counts as not imported | `release_placement` | `releasePlacement` | - |

**An anime's files.** `list_anime_files` lists the managed videos of one anime with their bound subtitles and the result of the latest file scan per file: `aligned`, `misaligned` (with `target_path`), `missing`, `changed`, `no_target` or `unscanned`. A status exists only after a scan (`submitReconciliationScan`, HTTP only); read again while `scan.state` is `running`. Rows carry `group_name` (the release's subtitle group) and paths relative to `root`. Each row lists its `actions` with `available` and a `reason_code` when not. A misaligned file is moved once by `align_anime_files` (pass the `generation` you read, and optionally `placement_ids`), whether or not automatic alignment is on; the collision setting applies, and a newer scan answers `ANIME_FILES_ALIGN_UNAVAILABLE` (409) with `scan_changed` in `details[0].code`. A missing or changed file is re-run with `retry_release` on its `release_id`, or released with `release_placement`; ask the human first.

Both need an `Idempotency-Key` and answer `PLACEMENT_EXIT_UNAVAILABLE` (409) with the reason in `details[0].code` (`placement_not_active`, `file_not_missing`, `source_release_unavailable`, or a release retry reason such as `download_in_progress`) when the file does not admit the exit now. A pending request (`redownload_requested`) does not block either exit: under the skip collision setting a new file whose target is taken is not placed, and the old placement stays active until the human acts again.

Browsing is HTTP only and goes by absolute path: `listFileManagerMounts` returns the quick entries (the filesystem root, each downloader's application-side root, each anime's directory), and `listFileManagerEntries` lists one directory by `path`, paged with `cursor`. A mount is a navigation shortcut only and never bounds a read or a write, so every path you send must be absolute; a relative path, a path through a symbolic link, and a path inside an application-data or system directory are all refused. `getFileManagerEntry` reads one entry, and the other file operations and alignment are HTTP only too (`submitFileManagerOperation`, `getFileManagerOperation`, `alignFileManagerEntry`). A changed file or target is refused with `FILE_MANAGER_ENTRY_CHANGED` or `FILE_MANAGER_MATCH_TARGET_CHANGED` and nothing is written: preview again. Writing to a directory that holds files the product manages is refused with `FILE_MANAGER_ENTRY_LOCKED`, whose `details` names the locked paths.

### 5.4 Execution records (group `records`)

| Step | MCP tool | operationId | CLI |
|---|---|---|---|
| List accepted acquisitions, newest first (filters: `origin`, `managed_anime_id`, `state`, `entry_kind`, `from`, `to`; page with `cursor`) | `list_execution_records` | `listAcquisitionRecords` | - |
| Read one record: stages, outcomes, reasons | `get_execution_record` | `getAcquisitionRecord` | - |
| List the files of a full record | `list_execution_record_files` | `listAcquisitionRecordFiles` | - |
| Retry the failed import stage (send the record `version` as `expected_version`) | `retry_import` | `retryAcquisitionImport` | `bansoko retry-import` |

`list_execution_record_files` fails with `ACQUISITION_FILES_NOT_READY` before the file set is sealed and `ACQUISITION_DETAIL_UNAVAILABLE` once the record is compact.
Each record names its source `release` and, when the frozen plan numbers them, the `episodes` it places; a running download also carries `transfer` (bytes done, total, speed) from the last sync poll, about a minute old at most. A delivered file carries `placed_path`, where it landed relative to the library, and `collision_outcome` (`none`, `skipped`, `versioned`, `overwritten`) when the recorded facts say how the collision setting played out.
`origin=subscription` keeps automatic and subscription-add records and `origin=one_shot` keeps one-shot records; every page carries `origin_counts` (both totals under the anime, state and time filters).
`retry_import` places every failed file of the record again and reopens every failed placement handoff; succeeded files are untouched and nothing is downloaded again. It is accepted only when the record's `import_retry_available` is true: `IMPORT_RETRY_IN_FLIGHT` while the record still runs, `IMPORT_RETRY_NOT_APPLICABLE` when nothing in the import stage failed. The file_execution stage's `attempts_started` counts the runs.
A record with `entry_kind=one_shot` also carries `one_shot_id`, the marker handle to follow with `get_one_shot_acquisition` and `list_one_shot_attempts`; every other entry kind leaves it null.

### 5.5 Configuration (group `configuration`)

| Step | MCP tool | operationId | CLI |
|---|---|---|---|
| Trigger one manual dispatch run of an automation kind (`group_discovery`, `rss_refresh`, `metadata_match`, `catalog_refresh`) | `automation_run` | `runAutomation` | - |
| Read the Global business preset (sections, parser policy, retention, program references and `digest`) | `get_business_preset` | `getBusinessPreset` | - |

Connections, naming templates, global rule sections, schedules and API tokens are changed in the web UI or over HTTP (`bansoko api-token create --name N [--scopes read,write]`, `list`, `revoke` cover tokens). No MCP tool changes them. Update and bootstrap operations are not agent operations. Database backups are files: a token holding the secrets scope may download one (`GET /backup/export`). Restoring (`POST /backup/restore`, which replaces all data and restarts the service) is only for the owner signed in to the web UI: every API token gets 403 `WEB_SESSION_REQUIRED` whatever its scopes, and no MCP tool performs either. Rotating the secret encryption key or the session secret are server-side commands run by the owner at the deployment (`deployment/scripts/rotate-secret-key.sh`, `deployment/scripts/rotate-session-secret.sh`).

### Not in MCP

Stage retries other than release and import (`retryRSSSourceRuntime`, `retryDownloadTask`, `retryArtifact`; `bansoko retry --owner rss|download|artifact`), connection settings, notification channels, and the file manager beyond releasing and matching. Use HTTP or the CLI for these.

## 6. Repair is re-enqueue

When something failed, find the failed stage in `get_workspace` (or `get_execution_record`), then ask for that stage to run again. Do not create duplicates by re-subscribing.

| Symptom | Fix |
|---|---|
| Feed not fetched, or source unhealthy | `refresh_rss` |
| A release was filtered or held and rules or metadata changed since | `reevaluate_releases`, then `refresh_rss` |
| A release's download failed or never started, its post-processing failed, or its placed file is missing from the library | `retry_release` re-runs the whole chain under the current rules (refused with `RELEASE_NOT_RETRYABLE` while a download or processing is in progress, or while its files are in the library) |
| Anime held waiting for metadata | `patch_anime_metadata` or `match_metadata` |
| A managed file is missing or changed on disk | ask the human: `redownload_placement` or `release_placement` (section 5.3) |
| Files failed to import, or a placement handoff failed | `retry_import` on the execution record (only when `import_retry_available` is true) |
| RSS runtime, download task or artifact failed | `bansoko retry --owner ...` with the owner id and its `failed_work_item_id` from `bansoko status --json` |
| One-shot failed before import | admit the seed again as a new one-shot (`acquire_seed_once` or `download_snapshot_seed`) with a new `idempotency_key` |

If the same repair keeps failing, report the failed state, `code` and `reason` to the human instead of looping. A refusal such as `placement_inside_content` repeats until the library layout is fixed by a person.

## 7. Errors and exit codes

Every HTTP error body is `{"code", "message", "request_id", "current_version"?, "required_scope"?, "details"?}`. Codes you will meet:

| Code | HTTP | Meaning |
|---|---|---|
| `SESSION_REQUIRED`, `SESSION_INVALID` | 401 | No token, or the token is invalid, expired or revoked |
| `TOKEN_SCOPE_INSUFFICIENT` | 403 | The token lacks the scope named in `required_scope` |
| `REQUEST_INVALID` | 400 | Malformed request or unknown field |
| `IDEMPOTENCY_KEY_REQUIRED` | 400 | The key is missing or not 8 to 128 characters |
| `NOT_FOUND` | 404 | The owner does not exist |
| `OWNER_VERSION_CONFLICT` | 409 | Stale `expected_version`; `current_version` is the live one |
| `IDEMPOTENCY_KEY_REUSED` | 409 | Same key, different request |
| `DRAFT_INVALID` | 422 | The draft failed validation; `details` names the fields. Detail code `SECRET_PLACEHOLDER_UNRESOLVED`: a `locator` is still `[secret]` and no single stored source matches it |
| `BUSINESS_SNAPSHOT_REQUIRED` | 422 | The draft carries no business snapshot; pass it through `load_business_preset` |
| `BUSINESS_SNAPSHOT_INVALID` | 422 | The draft's business snapshot or its digest was changed by hand; load it again |
| `ORIGIN_NOT_SUBSCRIBABLE` | 422 | A draft with `origin: oneshot` cannot be subscribed |
| `SEARCH_NOT_SUPPORTED` | 422 | The provider exposes no keyword search at all |
| `PROVIDER_RATE_LIMITED` | 429 | The provider refused a keyword search (429/503) and supplied its own `Retry-After`; wait that long before searching again |
| `INPUT_SOURCE_INVALID` | 400 | The preview or one-shot input mixes sources or names none |
| `CANDIDATE_REVISION_REQUIRED` | 409 | The candidate has no saved revision yet |
| `CANDIDATE_ALREADY_ACTIVATED` | 409 | The candidate is already linked to an anime |
| `SEED_PREVIEW_STALE` | 422 | The referenced feed snapshot expired, was replaced, or does not contain the seed; refresh it and choose again |
| `SEED_INVALID` | 422 | The seed or seed reference is incomplete or malformed |
| `TORRENT_FETCH_FAILED` | 422 | The torrent could not be fetched or verified |
| `TMDB_BINDING_REQUIRED` | 409 | The anime has no TMDB TV binding |
| `RELEASE_NOT_RETRYABLE`, `IMPORT_RETRY_NOT_APPLICABLE` | 409 | The stage is not in a retryable state; the message says why |
| `IMPORT_RETRY_IN_FLIGHT` | 409 | The record still runs; wait until it settles |
| `ACQUISITION_FILES_NOT_READY` | 409 | The record's file set is not sealed yet |
| `ACQUISITION_DETAIL_UNAVAILABLE` | 410 | The record is compact; only its summary remains |
| `FILE_MANAGER_ENTRY_CHANGED`, `FILE_MANAGER_MATCH_TARGET_CHANGED` | 409 | The file or previewed target changed; preview again |
| `FILE_MANAGER_PATH_INVALID`, `FILE_MANAGER_NOT_A_DIRECTORY`, `FILE_MANAGER_SYMLINK_NOT_FOLLOWED`, `FILE_MANAGER_PATH_DENIED`, `FILE_MANAGER_CURSOR_INVALID` | 400 | The path is not an absolute browsable directory, passes through a symbolic link, or lies in a directory the browser refuses |
| `FILE_MANAGER_PATH_NOT_FOUND`, `FILE_MANAGER_LOCATION_UNKNOWN` | 404 | Nothing is there, or no browsable directory holds that anime's files |
| `FILE_MANAGER_PATH_UNREADABLE`, `FILE_MANAGER_ENTRY_LOCKED` | 409 | The directory cannot be read, or it holds files the product manages (`details` names them) |
| `WRITES_REJECTED` | 503 | Maintenance; writes are paused |
| `INTERNAL` | 500 | Unexpected server fault |

`bansoko` exit codes are fixed:

| Exit code | Meaning |
|---|---|
| 0 | Success |
| 1 | Any other failure (including a failed pipeline in `wait`, a `wait` timeout, other HTTP 4xx) |
| 2 | Usage error: bad flag, missing argument, unknown command |
| 3 | Authentication or permission: no credential, HTTP 401 or 403 |
| 4 | Not found: HTTP 404 or 410 |
| 5 | Conflict or precondition not met: HTTP 409, 412, 422 or 428 |
| 6 | Server unreachable, or a server fault: connection failure or HTTP 5xx |

With `--json` a failure still prints one document on stdout, `{"error":{"code","message","http_status","exit_code"}}`, so a script can branch on `exit_code` and `code`.

## Appendix: minimal custom-feed draft

The body of `create_candidate` (field `draft`) and the `draft` of `rss_list_view` (`current_input`) is an AnimeDraft. This one subscribes to a single RSS feed and leaves every rule section out, so it takes a copy of the Global defaults. It is the input of `load_business_preset`: send it as that tool's `draft`, then use the returned draft (the same fields plus `business` and `business_digest`, with every rules section filled in) for `rss_list_view`, `create_candidate` and activation.

```json
{
  "origin": "custom",
  "instance_label": "My show",
  "title": "My Show",
  "metadata": {
    "bgm": {"work_id": "400602"},
    "tmdb": {"work_id": "209867", "media_kind": "tv", "season": 1},
    "season": 1
  },
  "subgroups": {"entries": [], "max_rss": 3},
  "rules": {"sections": {}},
  "rss_sources": [
    {
      "source_kind": "custom_rss",
      "locator": "https://feeds.example.com/rss.xml",
      "group_name": "Some Group",
      "title": "My Show",
      "episode_offset": 0
    }
  ]
}
```

`metadata` may be empty (`{}`): the anime then waits in the metadata hold until bindings are set. A `tmdb` binding needs `work_id`, `media_kind` and `season` together. The top-level `season` is the season naming uses; leave it out for no season folder.
