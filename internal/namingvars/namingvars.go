// Package namingvars loads the deployment-owned JavaScript Vars program.
package namingvars

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
)

const (
	SchemaVersion = 1
	// ProfileBase is the Vars program runtime: programs read the release-level
	// fact input (no mediainfo).
	ProfileBase = "naming-vars-v1"
	// Limits are deliberately small because this is deployment-owned code run at startup.
	MaxSourceBytes   = 64 << 10
	MaxInputBytes    = 256 << 10
	MaxOutputBytes   = 8192
	MaxCollectionLen = 256
	MaxStringBytes   = 8192
	// DefaultTimeout is the fixed per-call wall-clock cap of one evaluation,
	// including compiling the script. It bounds a runaway script, not a
	// business budget: a slow or loaded host that hits it gets a transient
	// ErrEvaluationInterrupted, never a frozen evaluation error.
	DefaultTimeout = 500 * time.Millisecond
)

// ErrEvaluationInterrupted marks a run stopped by the time limit. It says
// nothing about the program or its input, so callers must not freeze it as a
// final evaluation error; they retry the stage instead.
var ErrEvaluationInterrupted = errors.New("naming vars execution timeout")

// IsInterrupted reports whether err is a transient time-limit interrupt.
func IsInterrupted(err error) bool { return errors.Is(err, ErrEvaluationInterrupted) }

var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
var varsAssignmentPattern = regexp.MustCompile(`\bvars\s*\.\s*[a-z][a-z0-9_]*\s*=`)

// Reject collection constructors and growth APIs before goja executes them;
// output validation otherwise happens too late to bound intermediate objects.
var unboundedExpansionPattern = regexp.MustCompile(`(?i)(?:new\s+)?(?:Array|Object|Map|Set|WeakMap|WeakSet)\s*\(|\b(?:Array|Object|Reflect|Map|Set)\s*\.\s*(?:from|assign|create|keys|values|entries|ownKeys)\s*\(|\.\s*(?:repeat|padStart|padEnd|concat|fill|push|unshift|splice|flat|flatMap|map|filter|reduce)\s*\(|\.\.\.`)
var unboundedExecutionPattern = regexp.MustCompile(`(?i)\b(?:function|class|eval|with|import)\b|=>|\b(?:Object|Reflect)\s*\.`)

// OutputError is a strict-evaluation refusal of one program output value. It
// names the group and key so an editor can point at the assignment; the
// message is the frozen diagnostic text and never changes.
type OutputError struct {
	Group, Key string
	message    string
}

func (e *OutputError) Error() string { return e.message }

var reservedGroups = map[string]struct{}{"input": {}}

type Vars map[string]map[string]string

func (v Vars) Validate() error {
	if v == nil {
		return errors.New("vars must be an object")
	}
	if len(v) > MaxCollectionLen {
		return fmt.Errorf("vars exceeds %d groups", MaxCollectionLen)
	}
	var total int
	for group, values := range v {
		if !keyPattern.MatchString(group) {
			return fmt.Errorf("vars group %q has invalid name", group)
		}
		if _, reserved := reservedGroups[group]; reserved {
			return fmt.Errorf("vars group %q is reserved", group)
		}
		if values == nil {
			return fmt.Errorf("vars group %q must be an object", group)
		}
		if len(values) > MaxCollectionLen {
			return fmt.Errorf("vars group %q exceeds %d values", group, MaxCollectionLen)
		}
		for name, value := range values {
			if !keyPattern.MatchString(name) {
				return fmt.Errorf("vars %s.%q has invalid name", group, name)
			}
			if len(value) > MaxStringBytes {
				return fmt.Errorf("vars %s.%s exceeds %d bytes", group, name, MaxStringBytes)
			}
			total += len(value)
			if total > MaxOutputBytes {
				return fmt.Errorf("vars output exceeds %d bytes", MaxOutputBytes)
			}
		}
	}
	return nil
}

func Canonical(v Vars) ([]byte, error) {
	if err := v.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

// CanonicalSource returns the bounded canonical source bytes used as the
// immutable program identity. Newline normalization makes editor line-ending
// changes harmless while preserving all source semantics and comments.
func CanonicalSource(source string) ([]byte, error) {
	if len(source) == 0 {
		return nil, errors.New("naming vars source is empty")
	}
	if len(source) > MaxSourceBytes {
		return nil, fmt.Errorf("naming vars source exceeds %d bytes", MaxSourceBytes)
	}
	canonical := strings.ReplaceAll(source, "\r\n", "\n")
	canonical = strings.ReplaceAll(canonical, "\r", "\n")
	return []byte(canonical), nil
}

func SourceFingerprint(source string) (string, error) {
	b, err := CanonicalSource(source)
	if err != nil {
		return "", err
	}
	d := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(d[:]), nil
}

// Fingerprint identifies evaluated output for compatibility with callers that
// need a presentation digest. Published versions use SourceFingerprint instead.
func Fingerprint(v Vars) (string, error) {
	b, err := Canonical(v)
	if err != nil {
		return "", err
	}
	d := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(d[:]), nil
}
func ProfileVersion(fp string) string { return ProfileBase + "@" + fp }

// RuntimeOf reports the input contract a program reference was published
// for: ProfileBase when Version is exactly "<base>@<digest>", false otherwise.
func RuntimeOf(ref ProgramRef) (string, bool) {
	if ref.Digest == "" || ref.Version != ProfileBase+"@"+ref.Digest {
		return "", false
	}
	return ProfileBase, true
}

// Input is the release-level evidence every Vars call site passes: the
// release title, the exact torrent file inventory and the release fact
// object. Call sites build it with BuildInput; the program reads it through
// Payload, so two call sites with the same evidence evaluate the same input.
type Input struct {
	ReleaseTitle string `json:"release_title"`
	Files        []File `json:"files"`
	// Facts is the release fact object (input.anime, bgm,
	// tmdb, release, ep, torrent, rt, anibt; templatefacts VarsInputObject).
	// nil for an input frozen without it, whose program then reads only
	// release.title and the torrent totals.
	Facts map[string]any `json:"facts,omitempty"`
}
type File struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// DefaultInput is the bounded design input used during startup validation and
// when loading a source-backed legacy row from the database.
var DefaultInput = Input{
	ReleaseTitle: "[Sample] Example - 01 [1080p].mkv",
	Files:        []File{{Name: "Example - 01.mkv", Path: "Example - 01.mkv", Size: 1}},
}

// Payload is the `input` object a program reads:
//
//	input.release_title       the release title
//	input.files[]             {name, path, size} of every torrent file
//	input.release.title       the release title (null when blank)
//	input.torrent.size        total bytes of input.files (null without files)
//	input.torrent.file_count  number of input.files (null without files)
//	input.<root>              every Facts root (anime, bgm, tmdb, release, ep,
//	                          torrent, rt, anibt)
func (in Input) Payload() map[string]any {
	files := in.Files
	if files == nil {
		files = []File{}
	}
	var title, size, count any
	if strings.TrimSpace(in.ReleaseTitle) != "" {
		title = in.ReleaseTitle
	}
	if len(files) > 0 {
		var total int64
		for _, file := range files {
			total += file.Size
		}
		size, count = total, len(files)
	}
	release := map[string]any{}
	torrent := map[string]any{}
	payload := map[string]any{}
	for root, value := range in.Facts {
		object, ok := value.(map[string]any)
		if !ok || root == "release_title" || root == "files" || root == "mediainfo" {
			continue
		}
		copied := make(map[string]any, len(object))
		for key, member := range object {
			copied[key] = member
		}
		switch root {
		case "release":
			release = copied
		case "torrent":
			torrent = copied
		default:
			payload[root] = copied
		}
	}
	// release.title and the torrent totals are always derived from the
	// title and the files.
	release["title"] = title
	torrent["size"], torrent["file_count"] = size, count
	payload["release_title"] = in.ReleaseTitle
	payload["files"] = files
	payload["release"] = release
	payload["torrent"] = torrent
	return payload
}

// Run retains the legacy empty-input sample fallback for startup callers. It
// evaluates the current runtime's input.
func Run(source string, input Input, timeout time.Duration) (Vars, error) {
	if input.ReleaseTitle == "" && input.Files == nil {
		input = DefaultInput
	}
	return run(source, input.Payload(), timeout, false)
}

func run(source string, input map[string]any, timeout time.Duration, strict bool) (Vars, error) {
	canonicalSource, err := CanonicalSource(source)
	if err != nil {
		return nil, err
	}
	inputBytes, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("encode naming vars input: %w", err)
	}
	if len(inputBytes) > MaxInputBytes {
		return nil, fmt.Errorf("naming vars input exceeds %d bytes", MaxInputBytes)
	}
	if unboundedExpansionPattern.MatchString(string(canonicalSource)) {
		return nil, errors.New("naming vars unbounded expansion is not allowed")
	}
	if unboundedExecutionPattern.MatchString(string(canonicalSource)) {
		return nil, errors.New("naming vars dynamic execution is not allowed")
	}
	if assignments := len(varsAssignmentPattern.FindAllStringIndex(string(canonicalSource), -1)); assignments > MaxCollectionLen {
		return nil, fmt.Errorf("naming vars exceeds %d assignments", MaxCollectionLen)
	}
	r := goja.New()
	if strict {
		// Remove nondeterministic and dynamic-code capabilities, including computed aliases.
		if _, err := r.RunString(`Object.defineProperty(Function.prototype, "constructor", {value: undefined, writable: false, configurable: false}); Object.defineProperty(globalThis, "Function", {value: undefined, writable: false}); Object.defineProperty(globalThis, "eval", {value: undefined, writable: false}); Object.defineProperty(globalThis, "Date", {value: undefined, writable: false}); Object.defineProperty(Math, "random", {value: undefined, writable: false});`); err != nil {
			return nil, err
		}
	}
	// No host objects are exposed. The input is immutable and vars is the only
	// mutable output channel.
	inputValue, err := r.RunString("JSON.parse(" + strconv.Quote(string(inputBytes)) + ")")
	if err != nil {
		return nil, fmt.Errorf("prepare naming vars input: %w", err)
	}
	if err := r.Set("input", inputValue); err != nil {
		return nil, err
	}
	if _, err := r.RunString(`(function deepFreeze(o) { if (o && typeof o === "object") { Object.freeze(o); Object.keys(o).forEach(function(k) { deepFreeze(o[k]); }); } return o; })(input)`); err != nil {
		return nil, fmt.Errorf("freeze naming vars input: %w", err)
	}
	// vars is the only mutable output channel. The program may assign groups to it,
	// but it must never read one back: variables are independent by construction.
	// A text scan for `vars.` is not enough — `vars.a = {b: `${vars.b.c}`}` and
	// computed reads slip past a regex. The proxy below rejects every read
	// regardless of spelling, so "independence" is enforced where it is decided.
	varsTarget := r.NewObject()
	// Reading a value out of a group is what the rule forbids; locating a declared
	// group in order to assign into it is not. So a group lookup yields a second
	// write-only proxy: `vars.a.y = '2'` still works, `vars.a.x` never does.
	independenceError := func(property string) {
		panic(r.NewTypeError("naming vars variables must be independent; vars values cannot reference vars (" + property + ")"))
	}
	groupGuard := func(object *goja.Object) goja.Value {
		return r.ToValue(r.NewProxy(object, &goja.ProxyTrapConfig{
			Get: func(_ *goja.Object, property string, _ goja.Value) goja.Value {
				independenceError(property)
				return goja.Undefined()
			},
		}))
	}
	guard := r.NewProxy(varsTarget, &goja.ProxyTrapConfig{
		Get: func(target *goja.Object, property string, _ goja.Value) goja.Value {
			if value := target.Get(property); value != nil {
				if object, ok := value.(*goja.Object); ok && !goja.IsNull(value) && !goja.IsUndefined(value) {
					return groupGuard(object)
				}
			}
			independenceError(property)
			return goja.Undefined()
		},
	})
	if err := r.Set("vars", guard); err != nil {
		return nil, err
	}
	if err := r.Set("extract", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) < 2 {
			if strict {
				panic(r.NewTypeError("naming vars extract requires subject and pattern"))
			}
			return r.ToValue("")
		}
		if strict {
			if _, ok := call.Argument(0).(goja.String); !ok {
				panic(r.NewTypeError("naming vars extract subject must be a present string"))
			}
			pattern := call.Argument(1)
			if goja.IsNull(pattern) || goja.IsUndefined(pattern) {
				panic(r.NewTypeError("naming vars extract pattern is missing"))
			}
		}
		value := call.Argument(0).String()
		patternValue := call.Argument(1)
		pattern := patternValue.String()
		if object, ok := patternValue.(*goja.Object); ok {
			if source := object.Get("source"); source != nil && source != goja.Undefined() {
				pattern = source.String()
			}
		}
		if len(pattern) > MaxStringBytes {
			if strict {
				panic(r.NewTypeError("naming vars regex exceeds byte limit"))
			}
			return r.ToValue("")
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			if strict {
				panic(r.NewTypeError("naming vars regex is invalid"))
			}
			return r.ToValue("")
		}
		indices := re.FindStringSubmatchIndex(value)
		if len(indices) < 4 || indices[2] < 0 {
			if strict {
				// Missing capture is not a successful empty fact. The program
				// may explicitly choose a fallback via ??, || or a function.
				return goja.Null()
			}
			return r.ToValue("")
		}
		return r.ToValue(value[indices[2]:indices[3]])
	}); err != nil {
		return nil, err
	}
	if timeout <= 0 || timeout > DefaultTimeout {
		timeout = DefaultTimeout
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-time.After(timeout):
			r.Interrupt("naming vars execution timeout")
		case <-done:
		}
	}()
	executionSource := string(canonicalSource)
	if strict {
		executionSource, err = protectInterpolations(executionSource, r)
		if err != nil {
			return nil, fmt.Errorf("prepare naming vars interpolation: %w", err)
		}
		executionSource = strictPrefix + executionSource
	} else {
		// One leading line in both modes, so an engine position maps back to
		// the canonical source the same way (see EvaluationDiagnostic).
		executionSource = "\n" + executionSource
	}
	value, err := r.RunString(executionSource)
	if err != nil {
		var interrupted *goja.InterruptedError
		if errors.As(err, &interrupted) {
			return nil, fmt.Errorf("%w (%v)", ErrEvaluationInterrupted, timeout)
		}
		return nil, fmt.Errorf("naming vars execution failed: %w", err)
	}
	if value != nil && len(value.String()) > MaxOutputBytes {
		return nil, errors.New("naming vars output exceeds byte limit")
	}
	// Read the guard's target: the proxy is write-only by design, so exporting
	// through it would be reading the very values it refuses to expose.
	if strict {
		for _, group := range varsTarget.Keys() {
			object, ok := varsTarget.Get(group).(*goja.Object)
			if !ok {
				return nil, &OutputError{Group: group, message: "naming vars group must be an object"}
			}
			for _, key := range object.Keys() {
				value := object.Get(key)
				if value == nil || goja.IsNull(value) || goja.IsUndefined(value) {
					return nil, &OutputError{Group: group, Key: key, message: "naming vars output value is missing; choose an explicit fallback"}
				}
				if _, object := value.(*goja.Object); object {
					return nil, &OutputError{Group: group, Key: key, message: "naming vars output value must be a scalar"}
				}
			}
		}
	}
	var raw map[string]map[string]string
	if err := r.ExportTo(varsTarget, &raw); err != nil {
		return nil, fmt.Errorf("decode naming vars output: %w", err)
	}
	v := Vars(raw)
	if err := v.Validate(); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(v)
	if err != nil || len(encoded) > MaxOutputBytes {
		return nil, errors.New("naming vars output exceeds byte limit")
	}
	return v, nil
}

type Persistence interface {
	Publish(context.Context, string, string) error
	Current(context.Context) (string, string, error)
}

// Store is the application-facing persistence facade. It owns source
// validation/evaluation and active in-memory state; the concrete PostgreSQL
// adapter owns SQL and transaction lifecycle.
type Store struct {
	persistence Persistence
}

// NewStore wires an application store to its persistence port.
func NewStore(persistence Persistence) Store {
	return Store{persistence: persistence}
}

func (s Store) unavailable() error {
	if s.persistence == nil {
		return errors.New("naming vars database is nil")
	}
	return nil
}

func (s Store) PublishSource(ctx context.Context, source string, vars Vars, fp string) error {
	if err := s.unavailable(); err != nil {
		return err
	}
	canonicalSource, err := CanonicalSource(source)
	if err != nil {
		return err
	}
	canonicalFingerprint, err := SourceFingerprint(source)
	if err != nil {
		return err
	}
	if fp != canonicalFingerprint {
		return fmt.Errorf("naming vars fingerprint mismatch: supplied %q, canonical %q", fp, canonicalFingerprint)
	}
	if err := vars.Validate(); err != nil {
		return err
	}
	if err := s.persistence.Publish(ctx, string(canonicalSource), canonicalFingerprint); err != nil {
		return err
	}
	return SetActive(vars, canonicalFingerprint)
}

func (s Store) Current(ctx context.Context) (Vars, string, error) {
	if err := s.unavailable(); err != nil {
		return nil, "", err
	}
	source, fp, err := s.persistence.Current(ctx)
	if err != nil {
		return nil, "", err
	}
	canonicalSource, err := CanonicalSource(source)
	if err != nil {
		return nil, "", fmt.Errorf("validate active naming vars source: %w", err)
	}
	canonicalFingerprint, err := SourceFingerprint(string(canonicalSource))
	if err != nil {
		return nil, "", fmt.Errorf("fingerprint active naming vars: %w", err)
	}
	if fp != canonicalFingerprint {
		return nil, "", fmt.Errorf("active naming vars source fingerprint mismatch: stored %q, canonical %q", fp, canonicalFingerprint)
	}
	v, err := Run(string(canonicalSource), DesignInput(), 0)
	if err != nil {
		return nil, "", fmt.Errorf("evaluate active naming vars source: %w", err)
	}
	if err := SetActive(v, fp); err != nil {
		return nil, "", err
	}
	return v, fp, nil
}

var activeState struct {
	sync.RWMutex
	vars       Vars
	fp         string
	loadedAt   time.Time
	configured bool
}

func SetActive(v Vars, fp string) error {
	if err := v.Validate(); err != nil {
		return err
	}
	if fp == "" {
		return errors.New("naming vars fingerprint is empty")
	}
	activeState.Lock()
	activeState.vars, activeState.fp, activeState.loadedAt, activeState.configured = v, fp, time.Now().UTC(), true
	activeState.Unlock()
	return nil
}
func Current() (Vars, string, error) {
	vars, fp, _, err := CurrentMetadata()
	return vars, fp, err
}

func CurrentMetadata() (Vars, string, time.Time, error) {
	activeState.RLock()
	defer activeState.RUnlock()
	if !activeState.configured {
		return nil, "", time.Time{}, errors.New("naming vars have not been initialized")
	}
	return activeState.vars, activeState.fp, activeState.loadedAt, nil
}
func readSourceFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("naming vars source %s is not a regular file", path)
	}
	if info.Size() > MaxSourceBytes {
		return nil, fmt.Errorf("naming vars source exceeds %d bytes", MaxSourceBytes)
	}
	return os.ReadFile(path)
}

// InitializeResult reports how the active program was established at start.
type InitializeResult struct {
	Vars        Vars
	Fingerprint string
	// Seeded is true when no version existed and the deployment file (or the
	// bundled defaults) was published as the first active version.
	Seeded bool
	// FileDiverged is true when versions already existed and the deployment
	// file holds a different program. The file is then only a diagnostic:
	// the stored active version stays authoritative.
	FileDiverged bool
	FileDigest   string
}

// Initialize establishes the active program at start. The database owns the
// program: an existing active version is loaded as it is, and the file is
// read only to seed the very first version (or, when versions exist, to
// report that it differs). It never overwrites a stored version and never
// writes the file.
func Initialize(ctx context.Context, store Store, configPath, defaultsPath string) (Vars, string, error) {
	result, err := InitializeDetailed(ctx, store, configPath, defaultsPath)
	return result.Vars, result.Fingerprint, err
}

// InitializeDetailed is Initialize with the seeding and divergence report.
func InitializeDetailed(ctx context.Context, store Store, configPath, defaultsPath string) (InitializeResult, error) {
	var result InitializeResult
	if strings.TrimSpace(configPath) == "" || strings.TrimSpace(defaultsPath) == "" {
		return result, errors.New("naming vars config and defaults paths are required")
	}
	versioned, err := store.versioned()
	if err != nil {
		return result, err
	}
	_, activeErr := versioned.ActiveVersion(ctx)
	if activeErr != nil && !errors.Is(activeErr, ErrVersionNotFound) {
		return result, activeErr
	}
	// The deployment file is the seed (or import source); without one the
	// bundled defaults are.
	seedPath, fromFile := configPath, true
	if _, err := os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
		seedPath, fromFile = defaultsPath, false
	} else if err != nil {
		return result, err
	}
	if activeErr == nil {
		vars, fp, err := store.Current(ctx)
		if err != nil {
			return result, err
		}
		result.Vars, result.Fingerprint = vars, fp
		if fromFile {
			if b, err := readSourceFile(configPath); err == nil {
				if fileFP, err := SourceFingerprint(string(b)); err == nil && fileFP != fp {
					result.FileDiverged, result.FileDigest = true, fileFP
				}
			}
		}
		return result, nil
	}
	b, err := readSourceFile(seedPath)
	if err != nil {
		if !fromFile {
			return result, fmt.Errorf("read naming vars defaults: %w", err)
		}
		return result, err
	}
	vars, diagnostics := Validate(string(b))
	if len(diagnostics) > 0 {
		return result, fmt.Errorf("%s: %s", seedPath, diagnostics[0].Message)
	}
	canonical, err := CanonicalSource(string(b))
	if err != nil {
		return result, err
	}
	fp, err := SourceFingerprint(string(b))
	if err != nil {
		return result, err
	}
	if _, err := versioned.Seed(ctx, SaveParams{Source: string(canonical), Fingerprint: fp, Origin: OriginFile}); err != nil {
		return result, err
	}
	// A concurrent starter may have seeded another program first; the stored
	// active version is the authority either way.
	vars, fp, err = store.Current(ctx)
	if err != nil {
		return result, err
	}
	result.Vars, result.Fingerprint, result.Seeded = vars, fp, true
	return result, nil
}
