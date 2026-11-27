package buildinfo

import (
	"errors"
	"runtime"
	"strconv"
	"sync"
)

const (
	CISeries  = "bansoko-ci-v1"
	Unordered = "unordered"
)

// Order is comparable only within the same series and lane.
type Order struct {
	Series          string `json:"series,omitempty"`
	Lane            string `json:"lane,omitempty"`
	ReleaseSequence int64  `json:"release_sequence,omitempty"`
}

func (o Order) Ordered() bool {
	return o.Series != "" && (o.Lane == "stable" || o.Lane == "test") && o.ReleaseSequence > 0
}

func ParseOrder(series, lane, sequence string) (Order, error) {
	if series == "" && lane == "" && sequence == "" {
		return Order{Lane: Unordered}, nil
	}
	n, err := strconv.ParseInt(sequence, 10, 64)
	o := Order{Series: series, Lane: lane, ReleaseSequence: n}
	if err != nil || strconv.FormatInt(n, 10) != sequence || !o.Ordered() {
		return Order{Lane: Unordered}, errors.New("invalid release series, lane or sequence")
	}
	return o, nil
}

// BuildInfo is the redacted, immutable identity exposed by the target runtime.
// Release builds replace these variables at link time.
type BuildInfo struct {
	Order
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
	Series                   = ""
	Lane                     = ""
	ReleaseSequence          = ""
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

var linkedOrder = sync.OnceValues(func() (Order, error) {
	order, err := ParseOrder(Series, Lane, ReleaseSequence)
	if err == nil && order.Ordered() && (Version == "dev" || order.Series != CISeries) {
		return Order{Lane: Unordered}, errors.New("invalid ordered build version or series")
	}
	return order, err
})

func Validate() error {
	_, err := linkedOrder()
	return err
}

func Current() BuildInfo {
	order, _ := linkedOrder()
	return BuildInfo{
		Order:                    order,
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
