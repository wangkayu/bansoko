package application

import (
	"encoding/json"
	"testing"

	protocol "bansoko/internal/acquisition/terminal/domain"
	"bansoko/internal/episodeidentity"
	plugintargetport "bansoko/internal/plugintarget/port"
	"bansoko/internal/templatecatalog/notification"
)

// Every payload key of the file triggers is a key the producer writes;
// the typed success path adds the frozen input and episode facts.
func TestFileNotificationPayloadCoversCatalog(t *testing.T) {
	for _, trigger := range []string{plugintargetport.TriggerFileOperationCompleted, plugintargetport.TriggerFileOperationFailed} {
		fields := fileOperationNotificationFacts{frozen: notification.InputFacts(nil)}
		fields.frozen["task.origin"] = notification.TaskOrigin("")
		for key, value := range notification.EpisodeFacts(nil) {
			fields.frozen[key] = value
		}
		raw, err := buildFileOperationNotificationPayload(fields, trigger, "success", "file_operation_completed", "")
		if err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatal(err)
		}
		for _, key := range notification.PayloadKeys(trigger) {
			if key == "event.at" || key == "release.site" {
				continue // filled by the resolver from the ledger time and frozen source evidence
			}
			if _, ok := payload[key]; !ok {
				t.Errorf("%s: payload never writes %s", trigger, key)
			}
		}
	}
}

func TestSuccessfulFileNotificationProjectsPerFileSpanEpisodeFromMediaFacts(t *testing.T) {
	input := json.RawMessage(`{
		"schema_version": 1,
		"source": {
			"release": {"state": "known", "value": {"ref": "rel-1", "version": 1}, "reason_code": null},
			"title": {"state": "known", "value": "[LoliHouse] Show [01-12+EX 合集]", "reason_code": null},
			"group": {"state": "known", "value": "LoliHouse", "reason_code": null},
			"provenance": {"provider": "custom"},
			"infohash_v1": {"state": "absent", "value": null, "reason_code": null},
			"infohash_v2": {"state": "absent", "value": null, "reason_code": null},
			"torrent_size": {"state": "known", "value": 1024, "reason_code": null},
			"torrent_file_count": {"state": "known", "value": 12, "reason_code": null}
		},
		"config": {
			"identity": {"kind": "saved_revision", "managed_anime_ref": {"state": "known", "value": "anime-1", "reason_code": null}},
			"vars": {"output": {}}
		},
		"media": {
			"bgm": {"state": "absent", "value": null, "reason_code": null},
			"tmdb": {"state": "absent", "value": null, "reason_code": null},
			"identity": {"state": "absent", "value": null, "reason_code": null}
		},
		"delivery": {"operation": "hardlink"}
	}`)
	fact, err := protocol.ParseStrict([]byte(`{
		"identity_coverage": {"identity": {"state": "absent", "value": null, "reason_code": null}},
		"render_inputs": {"binding": {"state": "known", "value": {"effective_title": {"state": "known", "value": "我与尼特女忍者的莫名同居生活", "reason_code": null}}, "reason_code": null}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := protocol.ParseStrict([]byte(`{
		"publish_target": {"proof": {"target": {"location": {"canonical_path": "/library/Show/Season 01/Show S01E23-E24.mkv"}}}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	identity := episodeidentity.NewResolver().Resolve(episodeidentity.EpisodeIdentityInput{
		MediaKind: "tv", Span: 2,
		FileEvidence: episodeidentity.NewValueEvidence(episodeidentity.EpisodeSourceFileParser, "file_name", "12"),
	})
	fileIdentity, err := json.Marshal(identity)
	if err != nil {
		t.Fatal(err)
	}
	releaseIdentity, err := json.Marshal(episodeidentity.NewManualSingleIdentity("release", 1))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := successfulFileNotificationPayload(input, artifactFileContext{
		relativePath: "[LoliHouse] NEET Kunoichi - 12.mkv",
		sizeBytes:    1024,
		mediaKind:    "video",
	}, fact, checkpoint, "block", "automatic", nil, nil, fileIdentity, releaseIdentity)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["ep.raw"] != "23" || payload["ep.number"] != "23" || payload["ep.end"] != "24" || payload["ep.display"] != "23-24" || payload["ep.offset"] != float64(0) {
		t.Fatalf("completed file notification ep.* = %#v", payload)
	}
	if payload["out.name"] != "Show S01E23-E24.mkv" {
		t.Fatalf("out.name = %#v", payload["out.name"])
	}
}
