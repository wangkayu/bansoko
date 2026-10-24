package main

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"strings"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/acquisition/target"
	protocol "bansoko/internal/acquisition/terminal/domain"
	"bansoko/internal/batchplan"
	"bansoko/internal/episodeidentity"
	"bansoko/internal/filekinds"
	"bansoko/internal/parser"
	"bansoko/internal/testutil/frozenpolicy"
	"bansoko/internal/testutil/nativeinput"
)

// This command prints the fixture documents the file-maintenance e2e lane seeds
// with. The acceptance input comes from the same golden vector the Go fixtures
// bind their admission digest to, and the release facts carry the same batch
// plan an admission freezes, so the lane cannot drift from either schema in
// shell.
func main() {
	releaseTitle := flag.String("title", "[示例字幕组] 进击的巨人 - 01 [1080p][chs]", "release title frozen as source.title")
	fileName := flag.String("file", "EP01.mkv", "the video file name the facts resolve")
	mediaKind := flag.String("media-kind", "tv", "release media kind the identity and batch plan resolve under (tv|movie)")
	payloadDigest := flag.String("payload-digest", "", "payload digest bound to source.payload_digest")
	manifestDigest := flag.String("manifest-digest", "", "manifest digest bound to source.manifest_digest")
	routeRoot := flag.String("route-root", "", "delivery route root the plan renders targets under")
	flag.Parse()
	if *mediaKind != "tv" && *mediaKind != "movie" {
		panic("media-kind must be tv or movie")
	}
	season, seasonBound := 1, true
	if *mediaKind == "movie" {
		season, seasonBound = 0, false
	}
	ctx := context.Background()

	input, err := nativeinput.GoldenInput()
	if err != nil {
		panic(err)
	}
	source := input.Object["source"].Object
	source["kind"] = protocol.Value{Kind: protocol.KindString, String: "automatic_source"}
	source["title"] = protocol.Value{Kind: protocol.KindObject, Object: map[string]protocol.Value{
		"state":       {Kind: protocol.KindString, String: "known"},
		"value":       {Kind: protocol.KindString, String: *releaseTitle},
		"reason_code": {Kind: protocol.KindNull},
	}}
	if *payloadDigest != "" {
		source["payload_digest"] = protocol.Value{Kind: protocol.KindString, String: *payloadDigest}
	}
	if *manifestDigest != "" {
		source["manifest_digest"] = protocol.Value{Kind: protocol.KindString, String: *manifestDigest}
	}
	if *routeRoot != "" {
		input.Object["delivery"].Object["route"].Object["root"] = protocol.Value{Kind: protocol.KindObject, Object: map[string]protocol.Value{
			"state":       {Kind: protocol.KindString, String: "known"},
			"value":       {Kind: protocol.KindString, String: *routeRoot},
			"reason_code": {Kind: protocol.KindNull},
		}}
	}
	// The golden vector's config and naming sections demonstrate wire values,
	// not executable policy. The frozen business fixture owns the real rules:
	// overlay its effective sections onto the accepted config and its naming
	// rules onto the delivery, with the route root replacing the baked
	// library root so the render stays under the harness tree.
	policy := frozenpolicy.MustJSON()
	if *routeRoot != "" {
		var frozen struct {
			Sections map[string]json.RawMessage `json:"effective_sections"`
		}
		if err := json.Unmarshal([]byte(policy), &frozen); err != nil {
			panic(err)
		}
		overlaySection := func(base protocol.Value, rules json.RawMessage) protocol.Value {
			merged, err := json.Marshal(overlayJSON(base, rules, *routeRoot))
			if err != nil {
				panic(err)
			}
			patched, err := protocol.ParseStrict(merged)
			if err != nil {
				panic(err)
			}
			return patched
		}
		sections := input.Object["config"].Object["sections"].Array
		for _, entry := range sections {
			key := entry.Object["section_key"].String
			if value, ok := frozen.Sections[key]; ok {
				entry.Object["value"] = overlaySection(entry.Object["value"], value)
			}
		}
		if rules, ok := frozen.Sections["naming_rules"]; ok {
			templates := input.Object["delivery"].Object["naming"].Object["templates"]
			input.Object["delivery"].Object["naming"].Object["templates"] = overlaySection(templates, rules)
		}
	}
	if err := protocol.Validate("AcquisitionInputV1", input); err != nil {
		panic(err)
	}
	evidence, err := target.ResolveParserEvidence(ctx, target.ParserSelectionInput{
		Kind: string(parser.ParserRegex), EpisodePatterns: acquisitiondomain.DefaultEpisodePatterns,
		MediaKind: *mediaKind, Season: season, SeasonBound: seasonBound,
	}, *releaseTitle)
	if err != nil {
		panic(err)
	}
	identity, _, err := target.ResolveFileEpisode(ctx, evidence, *fileName)
	if err != nil {
		panic(err)
	}
	if identity.State != episodeidentity.StateResolved {
		panic("fixture video did not resolve: " + *fileName)
	}
	plan, err := batchplan.Classify(batchplan.Input{
		FileKindsVersion:  filekinds.Version,
		UnclassifiedRoute: batchplan.RouteSkip,
		Movie:             *mediaKind == "movie",
		MovieIdentity:     evidence.Identity,
		Files:             []batchplan.InputFile{{SourcePath: *fileName}},
		Identity: func(string) (episodeidentity.EpisodeIdentity, error) {
			return identity, nil
		},
	})
	if err != nil {
		panic(err)
	}

	// The golden vector's `media.tmdb` binding is a TV sample (kind tv, season
	// 0, work_ref tmdb-1) and the section overlay above only rewrites the
	// config sections, not this binding. The naming render reads the binding
	// frozen at acceptance, so a movie request must freeze a movie binding or
	// the render drifts to the TV branch (run 22: TV/... S00E00 instead of
	// Movie/...). Keep the sample work_ref; the kind and season follow the
	// request.
	if *mediaKind == "movie" {
		binding := input.Object["media"].Object["tmdb"].Object["value"].Object
		binding["kind"] = protocol.Value{Kind: protocol.KindString, String: "movie"}
		binding["season"] = protocol.Value{Kind: protocol.KindInteger, Int: int64(season)}
	}

	fixture := map[string]any{
		"frozen_policy":     policy,
		"acquisition_input": string(protocol.Canonical(input)),
		"identity":          identity,
		"release_facts": map[string]any{
			"title": *releaseTitle, "group_name": "示例字幕组", "resolution": "1080p",
			"languages": []string{"chs"}, "media_kind": *mediaKind, "season": season, "season_bound": seasonBound,
			"file_kinds_version": filekinds.Version, "batch_plan": plan,
		},
		"file_evidence":   map[string]any{"name": *fileName, "media_kind": "video", "size_bytes": 65536, "group_name": "示例字幕组", "resolution": "1080p", "languages": []string{"chs"}},
		"effective_facts": map[string]any{"identity": identity, "mapping_version": "facts-map-v1"},
	}
	if err := json.NewEncoder(os.Stdout).Encode(fixture); err != nil {
		panic(err)
	}
}

// overlayJSON deep-merges the frozen section onto the golden value: the
// golden document keeps the schema-required keys the fixture does not own,
// and the frozen rules win where both define a key. Naming directory
// templates get their baked "/media" library root rewritten to the route
// root, so the accepted render lands inside the harness tree.
func overlayJSON(base protocol.Value, rules json.RawMessage, routeRoot string) map[string]any {
	var merged map[string]any
	if err := json.Unmarshal(protocol.Canonical(base), &merged); err != nil {
		panic(err)
	}
	var frozenMap map[string]any
	if err := json.Unmarshal(rules, &frozenMap); err != nil {
		panic(err)
	}
	mergeInto(merged, frozenMap)
	for _, key := range []string{"tv", "movie", "unknown"} {
		variant, ok := merged[key].(map[string]any)
		if !ok {
			continue
		}
		if template, ok := variant["directory_template"].(string); ok {
			variant["directory_template"] = strings.Replace(template, "/media", routeRoot, 1)
		}
	}
	return merged
}

func mergeInto(base, overlay map[string]any) {
	for key, value := range overlay {
		if child, ok := value.(map[string]any); ok {
			if existing, ok := base[key].(map[string]any); ok {
				mergeInto(existing, child)
				continue
			}
		}
		base[key] = value
	}
}
