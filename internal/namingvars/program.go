package namingvars

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dop251/goja"
)

// ErrProgramUnavailable never permits substitution with the active program.
var ErrProgramUnavailable = errors.New("naming vars immutable program unavailable")

// ProgramRef pins canonical source bytes, not sample output. Version uses the
// existing ProfileVersion convention; Digest is the persisted source fingerprint.
type ProgramRef struct {
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

// Program is immutable and safe to reuse concurrently. Evaluation creates a new
// isolated VM on every call and never reads or changes the active Vars snapshot.
type Program struct {
	ref     ProgramRef
	runtime string
	source  string
}

func (p Program) Ref() ProgramRef { return p.ref }

// RuntimeVersion is the input contract the program was published for
// (ProfileBase).
func (p Program) RuntimeVersion() string { return p.runtime }

func NewProgram(ref ProgramRef, source string) (Program, error) {
	runtime, ok := RuntimeOf(ref)
	if !ok {
		return Program{}, errors.New("naming vars program version/digest mismatch")
	}
	canonical, err := CanonicalSource(source)
	if err != nil {
		return Program{}, err
	}
	fp, err := SourceFingerprint(string(canonical))
	if err != nil {
		return Program{}, err
	}
	if fp != ref.Digest {
		return Program{}, errors.New("naming vars program source digest mismatch")
	}
	if _, err := goja.Compile("naming-vars", string(canonical), false); err != nil {
		return Program{}, fmt.Errorf("compile naming vars program: %w", err)
	}
	return Program{ref: ref, runtime: runtime, source: string(canonical)}, nil
}

// Evaluate uses exactly the caller's historical Input envelope, even when empty.
// Null/undefined outputs require caller-authored fallback; scalar values retain
// the legacy two-level string output. This is trusted administrator code, not a
// hard memory sandbox. The timeout is capped; a plan-wide budget belongs to the
// caller. No current_file protocol or new fact collector is implied here.
func (p Program) Evaluate(input Input, timeout time.Duration) (Vars, error) {
	if p.source == "" {
		return nil, ErrProgramUnavailable
	}
	if timeout <= 0 || timeout > DefaultTimeout {
		timeout = DefaultTimeout
	}
	return run(p.source, input.Payload(), timeout, true)
}

// HistoricalSourceReader is an optional extension, so existing Persistence
// implementations and Store.Current startup behavior remain source-compatible.
type HistoricalSourceReader interface {
	SourceByFingerprint(context.Context, string) (string, error)
}

// LoadProgram reads only the exact persisted source version. No publication,
// activation, default-input evaluation, or current-version fallback occurs.
func (s Store) LoadProgram(ctx context.Context, ref ProgramRef) (Program, error) {
	if err := s.unavailable(); err != nil {
		return Program{}, err
	}
	if _, ok := RuntimeOf(ref); !ok {
		return Program{}, errors.New("naming vars program version/digest mismatch")
	}
	reader, ok := s.persistence.(HistoricalSourceReader)
	if !ok {
		return Program{}, ErrProgramUnavailable
	}
	source, err := reader.SourceByFingerprint(ctx, ref.Digest)
	if err != nil {
		return Program{}, fmt.Errorf("load naming vars program: %w", err)
	}
	return NewProgram(ref, source)
}
