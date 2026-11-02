// Package titleparser is the application read model for the reltitle parser
// component: which build answers a parse right now, where its binary lives,
// and the published channel it can be updated from.
//
// The service owns the observable state (the last channel answer and why an
// attempt failed) while reltitletags owns the running backend. An install is
// only adopted after the same probe chain startup uses, so a downloaded file
// that cannot answer never replaces a working parser.
package titleparser

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"bansoko/internal/parser/reltitletags"
	"bansoko/internal/rtruntime"
)

// EnvLane selects the reltitle channel lane, mirroring bansoko's own update
// lane. An empty value is the stable lane.
const EnvLane = "BANSOKO_UPDATE_LANE"

// Lane values the status reports.
const (
	LaneStable = "stable"
	LaneTest   = "test"
)

// Source values describe where the running parser came from.
const (
	SourceBuiltin = rtruntime.SourceBuiltin
	SourceManual  = rtruntime.SourceManual
	SourceChannel = rtruntime.SourceChannel
)

var (
	// ErrChannel reports that the published channel could not be read, decoded
	// or downloaded. The transport answers it as an upstream failure.
	ErrChannel = rtruntime.ErrChannel

	// ErrUpdate reports that a downloaded build could not be adopted. The
	// previous backend keeps running.
	ErrUpdate = errors.New("reltitle parser update")
)

// Notes is one channel release's notes in both languages.
type Notes struct {
	ZhCN string
	EN   string
}

// ChannelStatus is the last known answer from the published channel. A failed
// check updates CheckedAt and Error but keeps the previous successful version
// and notes, so the read model never claims less than it knew.
type ChannelStatus struct {
	CheckedAt  time.Time
	Version    string
	Notes      Notes
	ReleaseURL string
	// Newer is computed against the parser running when the status is read.
	Newer bool
	Error string
}

// Status is the parser component read model.
type Status struct {
	Source          string
	Version         string
	Line            string
	ConfigDigest    string
	BinaryPath      string
	Note            string
	ExternalFailure string
	Lane            string
	Channel         *ChannelStatus
	// Message reports the outcome of the most recent install attempt: why
	// nothing was installed, or what was installed. Empty before one runs.
	Message string
}

// FetchFunc reads one channel manifest. It exists so tests can point the
// service at an httptest channel without touching rtruntime's production root.
type FetchFunc func(ctx context.Context, client *http.Client, lane, line string) (*rtruntime.Channel, error)

// UpdateFunc performs one channel pass (read, download, verify, probe, replace).
type UpdateFunc func(ctx context.Context, options rtruntime.UpdateOptions) (rtruntime.UpdateResult, error)

// Parser is the process-wide reltitle adapter the service reads and hot-swaps.
type Parser interface {
	ActiveVersion() string
	ActiveDigest() string
	ExternalFailure() string
	// ProbeAndAdopt runs the adoption chain over the installed binary and
	// switches the active backend when it passes.
	ProbeAndAdopt(ctx context.Context, configRoot, builtinVersion string) error
}

// Options are the service's composition inputs. Fetch, Update and Parser are
// test seams; nil selects the production implementation.
type Options struct {
	ConfigRoot string
	HTTPClient *http.Client
	// Note is the startup probe's explanation (a refused manual build, an
	// older-than-builtin note, ...).
	Note string
	// Source is the startup probe's source for the running parser.
	Source string
	// Lane overrides the lane read from EnvLane.
	Lane string
	Now  func() time.Time

	Fetch  FetchFunc
	Update UpdateFunc
	Parser Parser
}

// Service serializes checks and installs. One lock is deliberate: the channel
// pass is short and two concurrent installs would race the same binary path.
type Service struct {
	mu sync.Mutex

	configRoot string
	client     *http.Client
	note       string
	source     string
	lane       string
	now        func() time.Time
	fetch      FetchFunc
	update     UpdateFunc
	parser     Parser

	channel *ChannelStatus
	message string
}

// New builds the service.
func New(options Options) *Service {
	lane := options.Lane
	if strings.TrimSpace(lane) == "" {
		lane = os.Getenv(EnvLane)
	}
	lane = normalizeLane(lane)
	source := options.Source
	switch source {
	case SourceBuiltin, SourceManual, SourceChannel:
	default:
		source = SourceBuiltin
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	fetch := options.Fetch
	if fetch == nil {
		fetch = rtruntime.FetchChannel
	}
	update := options.Update
	if update == nil {
		update = rtruntime.Update
	}
	parser := options.Parser
	if parser == nil {
		parser = reltitleParser{}
	}
	return &Service{
		configRoot: options.ConfigRoot,
		client:     options.HTTPClient,
		note:       options.Note,
		source:     source,
		lane:       lane,
		now:        now,
		fetch:      fetch,
		update:     update,
		parser:     parser,
	}
}

// normalizeLane maps a configured value onto a lane; unknown values are the
// stable lane so the read model always reports one of the two.
func normalizeLane(lane string) string {
	switch strings.ToLower(strings.TrimSpace(lane)) {
	case "", LaneStable:
		return LaneStable
	case LaneTest:
		return LaneTest
	default:
		return LaneStable
	}
}

// Status assembles the read model without touching the network.
func (s *Service) Status(ctx context.Context) (Status, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusLocked(), nil
}

// Check reads the published channel once and caches the answer. A failure is
// returned so the caller is never silent, but the previous successful answer
// stays visible in the read model.
func (s *Service) Check(ctx context.Context) (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.checkLocked(ctx)
}

func (s *Service) checkLocked(ctx context.Context) (Status, error) {
	checkedAt := s.now().UTC()
	current := s.runningVersion()
	line := rtruntime.LineOf(current)
	if line == "" {
		err := fmt.Errorf("%w: the running parser version %q has no major.minor line", ErrChannel, current)
		s.recordFailure(checkedAt, err)
		return s.statusLocked(), err
	}
	channel, err := s.fetch(ctx, s.client, s.lane, line)
	if err != nil {
		s.recordFailure(checkedAt, err)
		return s.statusLocked(), channelError(err)
	}
	s.recordChannel(checkedAt, channel)
	return s.statusLocked(), nil
}

// Install reads the channel and installs a strictly newer build. The new file
// is adopted only after the probe chain accepts it; a refusal leaves the
// previous backend running and records why.
func (s *Service) Install(ctx context.Context) (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	checkedAt := s.now().UTC()
	current := s.runningVersion()
	if line := rtruntime.LineOf(current); line == "" {
		err := fmt.Errorf("%w: the running parser version %q has no major.minor line", ErrChannel, current)
		s.recordFailure(checkedAt, err)
		return s.statusLocked(), err
	}
	result, err := s.update(ctx, rtruntime.UpdateOptions{
		ConfigRoot:     s.configRoot,
		Lane:           s.lane,
		Client:         s.client,
		CurrentVersion: current,
		BuiltinVersion: reltitletags.BuiltinVersion(),
		Now:            s.now,
	})
	if result.Channel.Version != "" {
		s.recordChannel(checkedAt, &result.Channel)
	}
	if err != nil {
		if errors.Is(err, rtruntime.ErrAdoption) {
			err = fmt.Errorf("%w: %v", ErrUpdate, err)
		}
		s.recordFailure(checkedAt, err)
		if errors.Is(err, ErrUpdate) {
			return s.statusLocked(), err
		}
		return s.statusLocked(), channelError(err)
	}
	if !result.Installed {
		s.message = result.Reason
		return s.statusLocked(), nil
	}
	if adoptErr := s.parser.ProbeAndAdopt(ctx, s.configRoot, reltitletags.BuiltinVersion()); adoptErr != nil {
		reason := fmt.Sprintf("reltitle %s was installed but the running parser was not switched: %v", result.Version, adoptErr)
		s.note = reason
		if s.channel == nil {
			s.channel = &ChannelStatus{CheckedAt: checkedAt}
		}
		s.channel.Error = reason
		s.message = reason
		return s.statusLocked(), fmt.Errorf("%w: %v", ErrUpdate, adoptErr)
	}
	s.source = SourceChannel
	s.note = ""
	s.message = "installed reltitle " + result.Version
	if result.Reason != "" {
		s.message += "; " + result.Reason
	}
	return s.statusLocked(), nil
}

// channelError keeps every external channel failure recognizable at the
// transport boundary, where it becomes a 502 rather than a generic 500.
func channelError(err error) error {
	if errors.Is(err, ErrChannel) {
		return err
	}
	return fmt.Errorf("%w: %v", ErrChannel, err)
}

// recordChannel stores one successful channel answer, clearing the last error.
func (s *Service) recordChannel(checkedAt time.Time, channel *rtruntime.Channel) {
	s.channel = &ChannelStatus{
		CheckedAt:  checkedAt,
		Version:    channel.Version,
		Notes:      Notes{ZhCN: channel.Notes.ZhCN, EN: channel.Notes.EN},
		ReleaseURL: channel.ReleaseURL,
	}
}

// recordFailure records why an attempt failed without discarding the last
// successful answer.
func (s *Service) recordFailure(checkedAt time.Time, err error) {
	if s.channel == nil {
		s.channel = &ChannelStatus{}
	}
	s.channel.CheckedAt = checkedAt
	s.channel.Error = err.Error()
}

func (s *Service) statusLocked() Status {
	version := s.parser.ActiveVersion()
	status := Status{
		Source:          s.runningSource(),
		Version:         version,
		Line:            rtruntime.LineOf(s.runningVersion()),
		ConfigDigest:    s.parser.ActiveDigest(),
		BinaryPath:      rtruntime.BinaryPath(s.configRoot),
		Note:            s.note,
		ExternalFailure: s.parser.ExternalFailure(),
		Lane:            s.lane,
		Message:         s.message,
	}
	if s.channel != nil {
		channel := *s.channel
		channel.Newer = channel.Version != "" && rtruntime.Newer(channel.Version, s.runningVersion())
		status.Channel = &channel
	}
	return status
}

// runningVersion is the version the updater compares against: the compiled-in
// module version while the library parses, otherwise the adopted build's
// version without the reltitle namespace.
func (s *Service) runningVersion() string {
	if s.runningSource() == SourceBuiltin {
		return reltitletags.BuiltinVersion()
	}
	return strings.TrimPrefix(s.parser.ActiveVersion(), "reltitle@")
}

// runningSource reflects the backend answering now. A mid-flight transport
// failure drops the adopted binary and falls back to the library, so the
// stored startup source cannot be reported on its own.
func (s *Service) runningSource() string {
	if s.parser.ActiveVersion() == reltitletags.ImplementationVersion {
		return SourceBuiltin
	}
	switch s.source {
	case SourceManual, SourceChannel:
		return s.source
	default:
		return SourceManual
	}
}

// reltitleParser is the production Parser.
type reltitleParser struct{}

func (reltitleParser) ActiveVersion() string   { return reltitletags.ActiveVersion() }
func (reltitleParser) ActiveDigest() string    { return reltitletags.ActiveDigest() }
func (reltitleParser) ExternalFailure() string { return reltitletags.ExternalFailure() }

func (reltitleParser) ProbeAndAdopt(ctx context.Context, configRoot, builtinVersion string) error {
	client, info := rtruntime.Probe(ctx, configRoot, builtinVersion)
	if client == nil {
		if strings.TrimSpace(info.Note) != "" {
			return errors.New(info.Note)
		}
		return fmt.Errorf("%s was not adopted", rtruntime.BinaryPath(configRoot))
	}
	if err := reltitletags.AdoptExternal(client); err != nil {
		_ = client.Close()
		return err
	}
	return nil
}
