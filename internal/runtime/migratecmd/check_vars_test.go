package migratecmd

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"bansoko/internal/namingvars/preflight"
)

func TestWriteCheckVarsReport(t *testing.T) {
	t.Run("clean text", func(t *testing.T) {
		var out, errOut bytes.Buffer
		if code := writeCheckVarsReport(nil, false, &out, &errOut); code != 0 {
			t.Fatalf("code = %d, want 0", code)
		}
		if !strings.Contains(out.String(), "ok") || errOut.Len() != 0 {
			t.Fatalf("stdout=%q stderr=%q", out.String(), errOut.String())
		}
	})

	t.Run("hits text", func(t *testing.T) {
		var out, errOut bytes.Buffer
		hits := []preflight.Hit{
			{Location: "global_config_sections:naming_rules", Field: "tv.video_filename_template"},
			{Location: "plugin_target_configs:t1", Field: "events.file_imported.body_template"},
		}
		if code := writeCheckVarsReport(hits, false, &out, &errOut); code != 1 {
			t.Fatalf("code = %d, want 1", code)
		}
		for _, want := range []string{"global_config_sections:naming_rules (tv.video_filename_template)", "plugin_target_configs:t1 (events.file_imported.body_template)"} {
			if !strings.Contains(errOut.String(), want) {
				t.Fatalf("stderr %q missing %q", errOut.String(), want)
			}
		}
	})

	t.Run("hits json", func(t *testing.T) {
		var out, errOut bytes.Buffer
		hits := []preflight.Hit{{Location: "naming_template_library:legacy", Field: "tv.directory_template"}}
		if code := writeCheckVarsReport(hits, true, &out, &errOut); code != 1 {
			t.Fatalf("code = %d, want 1", code)
		}
		var report checkVarsReport
		if err := json.Unmarshal(out.Bytes(), &report); err != nil {
			t.Fatalf("unmarshal json report: %v (%s)", err, out.String())
		}
		if report.OK || len(report.Hits) != 1 || report.Hits[0] != hits[0] {
			t.Fatalf("report = %+v, want ok=false hits=%+v", report, hits)
		}
	})
}

func TestRunCheckVarsRequiresDSN(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runCheckVars(context.Background(), nil, func(string) string { return "" }, &out, &errOut)
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "BANSOKO_DATABASE_URL") {
		t.Fatalf("stderr = %q, want missing DSN message", errOut.String())
	}
}

func TestRunCheckVarsHelp(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runCheckVars(context.Background(), []string{"-h"}, func(string) string {
		t.Fatal("help must not read the environment or require a database")
		return ""
	}, &out, &errOut)
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if !strings.Contains(errOut.String(), "Usage of check-vars") {
		t.Fatalf("stderr = %q, want flag usage", errOut.String())
	}
}

func TestCheckVarsDSNPrefersExplicitFlag(t *testing.T) {
	getenv := func(string) string { return "postgres://from-environment" }
	if got := checkVarsDSN(getenv, "postgres://from-flag"); got != "postgres://from-flag" {
		t.Fatalf("explicit DSN = %q, want flag value", got)
	}
	if got := checkVarsDSN(getenv, "  "); got != "postgres://from-environment" {
		t.Fatalf("fallback DSN = %q, want environment value", got)
	}
}
