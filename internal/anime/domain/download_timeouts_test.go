package domain

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestDownloadTimeoutsValidateBounds(t *testing.T) {
	if err := DefaultDownloadTimeouts().Validate(); err != nil {
		t.Fatalf("default refused: %v", err)
	}
	if d := DefaultDownloadTimeouts(); d.StallSeconds != 6*3600 || d.DownloadWaitSeconds != 7*86400 {
		t.Fatalf("default = %+v", d)
	}
	for _, ok := range []DownloadTimeouts{
		{StallSeconds: MinDownloadStallSeconds, DownloadWaitSeconds: MinDownloadWaitSeconds},
		{StallSeconds: MaxDownloadStallSeconds, DownloadWaitSeconds: MaxDownloadWaitSeconds},
		{StallSeconds: 86400, DownloadWaitSeconds: 86400},
	} {
		if err := ok.Validate(); err != nil {
			t.Fatalf("%+v refused: %v", ok, err)
		}
	}
	for _, c := range []struct {
		value DownloadTimeouts
		field string
	}{
		{DownloadTimeouts{StallSeconds: MinDownloadStallSeconds - 1, DownloadWaitSeconds: 86400}, "stall_seconds"},
		{DownloadTimeouts{StallSeconds: MaxDownloadStallSeconds + 1, DownloadWaitSeconds: MaxDownloadWaitSeconds}, "stall_seconds"},
		{DownloadTimeouts{StallSeconds: 3600, DownloadWaitSeconds: MinDownloadWaitSeconds - 1}, "download_wait_seconds"},
		{DownloadTimeouts{StallSeconds: 3600, DownloadWaitSeconds: MaxDownloadWaitSeconds + 1}, "download_wait_seconds"},
		{DownloadTimeouts{StallSeconds: 2 * 86400, DownloadWaitSeconds: 86400}, "stall_seconds"},
	} {
		var got *DownloadTimeoutsError
		if err := c.value.Validate(); !errors.As(err, &got) || got.Field != c.field {
			t.Fatalf("%+v: err=%v want field %s", c.value, err, c.field)
		}
	}
}

func TestDownloadTimeoutsSectionValue(t *testing.T) {
	if SectionSchemaVersions[SectionDownloadTimeouts] != DownloadTimeoutsSchemaVersion {
		t.Fatal("download_timeouts schema version is not registered")
	}
	value, err := DefaultSectionValue(SectionDownloadTimeouts)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"stall_seconds":21600,"download_wait_seconds":604800}` {
		t.Fatalf("default value = %s", raw)
	}
	if err := ValidateSectionValue(SectionDownloadTimeouts, raw); err != nil {
		t.Fatalf("default refused: %v", err)
	}
	for _, bad := range []string{
		`{}`,
		`{"stall_seconds":3600}`,
		`{"stall_seconds":3600,"download_wait_seconds":86400,"extra":1}`,
		`{"stall_seconds":"3600","download_wait_seconds":86400}`,
		`{"stall_seconds":3600.5,"download_wait_seconds":86400}`,
		`{"stall_seconds":172800,"download_wait_seconds":86400}`,
		`[]`,
	} {
		if err := ValidateSectionValue(SectionDownloadTimeouts, json.RawMessage(bad)); err == nil {
			t.Fatalf("%s accepted", bad)
		}
	}
	for _, name := range RulesOwnerSectionOrder {
		if name == SectionDownloadTimeouts {
			t.Fatal("download_timeouts must not be a rules-owner (overridable) section")
		}
	}
}
