package buildinfo

import (
	"errors"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

const (
	// UpstreamSeries is the default release series domain of the upstream
	// repository. Every repository (including forks) derives its own domain as
	// gh:<lowercase owner/repository>:v1, so two forks never share a sequence
	// space even though both count their own CI runs from 1.
	UpstreamSeries = "gh:wangkayu/bansoko:v1"
	// CISeries is the upstream domain, kept as the constant release tooling and
	// the upstream CI use when no repository is known.
	CISeries = UpstreamSeries
	// LegacySeries is the single hard-coded domain earlier builds used. It
	// stays readable so existing journals, fences and manifests remain
	// diagnosable, but nothing new is published under it.
	LegacySeries = "bansoko-ci-v1"
	Unordered    = "unordered"
)

// Order is comparable only within the same series and lane.
type Order struct {
	Series          string `json:"series,omitempty"`
	Lane            string `json:"lane,omitempty"`
	ReleaseSequence int64  `json:"release_sequence,omitempty"`
}

// DefaultSeries derives the release series domain of one repository. The value
// is lower case and bounded, so it can be compared byte for byte and compared
// across forks without any shared mutable state.
func DefaultSeries(repository string) string {
	repository = strings.ToLower(strings.Trim(strings.TrimSpace(repository), "/"))
	if repository == "" {
		return UpstreamSeries
	}
	series := "gh:" + repository + ":v1"
	if !ValidSeries(series) {
		return UpstreamSeries
	}
	return series
}

// ValidSeries reports whether a release series domain is canonical. Series
// values are lower case and whitespace free so an operator typo cannot create
// a second, silently incomparable sequence space.
var repositorySeries = regexp.MustCompile(`^gh:[a-z0-9][a-z0-9-]*/[a-z0-9._-]+:v1$`)

func ValidSeries(series string) bool {
	return len(series) <= 128 && repositorySeries.MatchString(series)
}

// KnownSeries includes the readable legacy sequence domain.
func KnownSeries(series string) bool {
	return series == LegacySeries || ValidSeries(series)
}

func (o Order) Ordered() bool {
	return KnownSeries(o.Series) && (o.Lane == "stable" || o.Lane == "test") && o.ReleaseSequence > 0
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
	// ContainerAnchor is linked only into the image's immutable entrypoint.
	ContainerAnchor          = ""
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
	if err == nil && order.Ordered() && (Version == "dev" || !ValidSeries(order.Series)) {
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
