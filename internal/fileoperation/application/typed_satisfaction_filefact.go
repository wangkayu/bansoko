package application

import (
	protocol "bansoko/internal/acquisition/terminal/domain"
	"bansoko/internal/episodeidentity"
	"bansoko/internal/postprocessing/naming"
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
)

// Self-contained historical projection for management; no current management
// lookup and no authority for subsequent filesystem changes.
func observedSatisfactionFileFact(b *artifactPreparedBatch, file artifactFileContext, evidence map[string]any) (string, error) {
	unknown := func() any { return frozenUnavailable("FACT_UNAVAILABLE") }
	source := map[string]any{}
	for _, key := range []string{"payload_digest", "manifest_digest", "infohash_v1", "infohash_v2", "file_ref", "original_name", "relative_path", "media_kind", "title", "group", "provider", "locator_display", "release", "source"} {
		source[key] = unknown()
	}
	source["file_ref"] = frozenKnown(file.fileRef)
	source["original_name"] = frozenKnown(filepath.Base(file.relativePath))
	source["relative_path"] = frozenKnown(file.relativePath)
	source["media_kind"] = frozenKnown(file.mediaKind)
	var capture struct {
		Artifact struct {
			Root string `json:"acquisition_id"`
		} `json:"artifact"`
		Witness *struct {
			Root   string `json:"acquisition_id"`
			Digest string `json:"witness_digest"`
			Value  struct {
				Observation struct {
					ObservedAt string `json:"observed_at"`
				} `json:"observation"`
			} `json:"witness"`
		} `json:"preparation_witness"`
	}
	if err := json.Unmarshal(b.capture.Facts, &capture); err != nil {
		return "", err
	}
	replay := map[string]any{}
	for _, key := range naming.ReplayFactPaths() {
		replay[key] = unknown()
	}
	provenance := map[string]any{"mode": "native", "acquisition_ref": frozenKnown(capture.Artifact.Root), "operation_ref": map[string]any{"state": "absent", "value": nil, "reason_code": nil}, "artifact_ref": frozenKnown(b.capture.ArtifactID), "artifact_file_ref": frozenKnown(file.id), "media_snapshot": unknown(), "delivery_digest": frozenKnown(evidence["digest"]), "exact_selection": unknown(), "missing_paths": []string{}}
	groups := map[string]any{"locator": map[string]any{"final_object": frozenKnown(evidence["final_object"]), "source_cleanup": "not_required"}, "content_source": source, "identity_coverage": map[string]any{"identity": unknown(), "coverage_schema": episodeidentity.ResolverVersion, "coverage_keys": []string{}}, "quality": unknown(), "render_inputs": map[string]any{"replay": replay, "binding": unknown(), "vars": unknown(), "naming_schema": unknown(), "naming_digest": unknown(), "render_program_version": unknown()}, "episode_offset": map[string]any{"offset": unknown(), "media_kind": unknown(), "season": unknown(), "season_bound": unknown(), "video_count": unknown()}, "provenance": provenance, "schema_version": 2}
	if capture.Witness != nil {
		if capture.Witness.Root != capture.Artifact.Root {
			return "", fmt.Errorf("PLAN_HANDOFF_INVALID")
		}
		provenance["exact_selection"] = frozenKnown(map[string]any{
			"relation": "acquisition_preparation_witness_v1", "row_ref": capture.Witness.Root,
			"version": frozenKnown(1), "digest": frozenKnown(capture.Witness.Digest),
			"observed_at": capture.Witness.Value.Observation.ObservedAt, "selection": "recorded_reference",
		})
	}
	var input map[string]json.RawMessage
	if err := json.Unmarshal(b.input, &input); err != nil {
		return "", err
	}
	var acceptedSource map[string]json.RawMessage
	json.Unmarshal(input["source"], &acceptedSource)
	for _, key := range []string{"payload_digest", "manifest_digest"} {
		if raw, ok := acceptedSource[key]; ok {
			source[key] = frozenKnown(raw)
		}
	}
	for _, key := range []string{"infohash_v1", "infohash_v2", "title", "group", "provider", "locator_display", "release", "source"} {
		if raw, ok := acceptedSource[key]; ok {
			source[key] = raw
		}
	}
	var media map[string]json.RawMessage
	json.Unmarshal(input["media"], &media)
	coverage := groups["identity_coverage"].(map[string]any)
	offset := groups["episode_offset"].(map[string]any)
	if raw, ok := media["identity"]; ok {
		coverage["identity"] = raw
		var fv struct {
			State string `json:"state"`
			Value struct {
				Keys            []string `json:"coverage_keys"`
				Offset          int64    `json:"offset"`
				ResolverVersion string   `json:"resolver_version"`
			} `json:"value"`
		}
		json.Unmarshal(raw, &fv)
		if fv.State == "known" {
			coverage["coverage_keys"] = fv.Value.Keys
			// The coverage schema is the contract the frozen identity was
			// resolved with: a sealed v1 identity keeps tv:s<S>:e<N> keys.
			if fv.Value.ResolverVersion == episodeidentity.LegacyResolverVersion {
				coverage["coverage_schema"] = episodeidentity.LegacyResolverVersion
			}
			// The one offset the frozen identity resolved with.
			offset["offset"] = frozenKnown(fv.Value.Offset)
		}
	}
	if raw, ok := media["quality"]; ok {
		groups["quality"] = raw
	}
	if raw, ok := media["tmdb"]; ok {
		var fv struct {
			State string `json:"state"`
			Value struct {
				Kind   string `json:"kind"`
				Season int64  `json:"season"`
			} `json:"value"`
		}
		if err := json.Unmarshal(raw, &fv); err != nil {
			return "", err
		}
		if fv.State == "known" {
			offset["media_kind"] = frozenKnown(fv.Value.Kind)
			offset["season"] = frozenKnown(fv.Value.Season)
			offset["season_bound"] = frozenKnown(true)
		}
	}
	videos := 0
	for _, f := range b.artifact.files {
		if f.mediaKind == "video" {
			videos++
		}
	}
	offset["video_count"] = frozenKnown(videos)
	var render struct {
		Facts   map[string]any `json:"render_facts"`
		Version string         `json:"renderer_version"`
		Binding map[string]any `json:"binding_facts"`
	}
	for _, op := range b.operations {
		if op.file.fileRef == file.fileRef {
			json.Unmarshal(op.plan.renderSnapshot, &render)
			break
		}
	}
	for key, value := range render.Facts {
		if _, ok := replay[key]; ok {
			replay[key] = frozenKnown(value)
		}
	}
	renderInputs := groups["render_inputs"].(map[string]any)
	vars, err := capturedVarsResult(b)
	if err != nil {
		return "", err
	}
	renderInputs["vars"] = vars
	if kind, ok := render.Binding["media_kind"].(string); ok && (kind == "tv" || kind == "movie") {
		binding := map[string]any{"media_kind": kind, "season": render.Binding["season"], "season_bound": render.Binding["season_bound"], "bgm_work_id": unknown(), "tmdb_work_id": unknown(), "effective_title": unknown(), "extension": unknown()}
		for key, sourceKey := range map[string]string{"bgm_work_id": "bgm_work_id", "tmdb_work_id": "tmdb_work_id", "effective_title": "effective_title", "extension": "extension"} {
			if value, ok := render.Binding[sourceKey].(string); ok {
				if value != "" || key == "extension" {
					binding[key] = frozenKnown(value)
				} else {
					binding[key] = map[string]any{"state": "absent", "value": nil, "reason_code": nil}
				}
			}
		}
		renderInputs["binding"] = frozenKnown(binding)
	}
	renderInputs["naming_schema"] = frozenKnown("naming-rules-v1")
	if render.Version != "" {
		renderInputs["render_program_version"] = frozenKnown(render.Version)
	}
	var delivery map[string]json.RawMessage
	json.Unmarshal(input["delivery"], &delivery)
	if raw, ok := delivery["naming"]; ok {
		v, err := protocol.ParseStrict(raw)
		if err != nil {
			return "", err
		}
		renderInputs["naming_digest"] = frozenKnown(protocol.Digest("acquisition-naming:v1", protocol.Canonical(v)))
	}
	// Decode raw accepted FrozenValues without float coercion before computing
	// missing_paths; their absent/unavailable states remain visible unchanged.
	for _, m := range []map[string]any{source, coverage, offset} {
		for key, value := range m {
			if raw, ok := value.(json.RawMessage); ok {
				v, err := protocol.ParseStrict(raw)
				if err != nil {
					return "", err
				}
				canonical := protocol.Canonical(v)
				var decoded any
				decoder := json.NewDecoder(bytes.NewReader(canonical))
				decoder.UseNumber()
				if err := decoder.Decode(&decoded); err != nil {
					return "", err
				}
				m[key] = decoded
			}
		}
	}
	paths := []string{}
	for group, value := range groups {
		m, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if m["state"] == "unavailable" {
			paths = append(paths, "/"+group)
		}
		for key, v := range m {
			fv, ok := v.(map[string]any)
			if ok && fv["state"] == "unavailable" {
				paths = append(paths, "/"+group+"/"+key)
			}
		}
	}
	sort.Strings(paths)
	provenance["missing_paths"] = paths
	raw, err := canonicalPlanValue(groups)
	if err != nil {
		return "", err
	}
	v, err := protocol.ParseStrict([]byte(raw))
	if err != nil {
		return "", err
	}
	if err := protocol.Validate("FileFactV2", v); err != nil {
		return "", err
	}
	return raw, nil
}
