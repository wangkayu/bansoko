package buildinfo

import "runtime"

// BuildInfo is the redacted, immutable identity exposed by the target runtime.
// Release builds replace these variables at link time.
type BuildInfo struct {
	Version       string `json:"version"`
	Commit        string `json:"commit"`
	BuildID       string `json:"build_id"`
	BuildTime     string `json:"build_time"`
	APIVersion    string `json:"api_version"`
	SchemaVersion string `json:"schema_version"`
	// BaseCompatibilityVersion is the compatibility label of the immutable
	// image, not the overlay release currently selected by the process.
	BaseCompatibilityVersion string `json:"base_compatibility_version"`
	GoVersion                string `json:"go_version"`
	Dirty                    bool   `json:"dirty"`
}

var (
	Version                  = "dev"
	Commit                   = "unknown"
	BuildID                  = "dev"
	BuildTime                = "unknown"
	APIVersion               = "v1"
	SchemaVersion            = "001_baseline"
	BaseCompatibilityVersion = "base-v1"
	BuildDirty               = "true"
	Dirty                    = false
)

func Current() BuildInfo {
	return BuildInfo{
		Version:                  Version,
		Commit:                   Commit,
		BuildID:                  BuildID,
		BuildTime:                BuildTime,
		APIVersion:               APIVersion,
		SchemaVersion:            SchemaVersion,
		BaseCompatibilityVersion: BaseCompatibilityVersion,
		GoVersion:                runtime.Version(),
		Dirty:                    Dirty || BuildDirty != "false",
	}
}

// UserAgent is the HTTP User-Agent sent to external providers by component
// (for example "catalog" or "metadata-matcher").
func UserAgent(component string) string {
	return "bansoko/" + Version + " (" + component + ")"
}
