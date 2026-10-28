package naming

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/parser"

	"bansoko/internal/templatefacts"
)

const (
	MaxRegexBytes          = 512
	MaxOutputBytes         = 8192
	MaxContextBytes        = 64 << 10
	MaxCollectionLen       = 256
	maxSubtitleSuffixBytes = 255
	// DefaultTimeout only stops a runaway template (an endless loop). It is
	// not a speed budget: a target path must not depend on how loaded the
	// host is, and an ordinary template finishes far inside it.
	DefaultTimeout = time.Second
)

const (
	ErrCodeTemplateCompileError    = "TEMPLATE_COMPILE_ERROR"
	ErrCodeTemplateLimitExceeded   = "TEMPLATE_LIMIT_EXCEEDED"
	ErrCodeTemplateTimeout         = "TEMPLATE_TIMEOUT"
	ErrCodeNamingVarsUnknown       = "NAMING_VARS_UNKNOWN"
	ErrCodeNamingTargetInvalid     = "NAMING_TARGET_INVALID"
	ErrCodeNamingTargetNotAbsolute = "NAMING_TARGET_NOT_ABSOLUTE"
	ErrCodeSubtitleSuffixInvalid   = "SUBTITLE_SUFFIX_INVALID"
)

type TemplateKind string

const (
	KindDirectory      TemplateKind = "directory"
	KindVideoFilename  TemplateKind = "video_filename"
	KindSubtitleSuffix TemplateKind = "subtitle_suffix"
)

type Error struct {
	Code, Message, Field string
	// Missing lists the absent facts of an ErrCodeNamingFactMissing refusal.
	Missing []string
}

func (e *Error) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("%s: %s: %s", e.Code, e.Field, e.Message)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Engine evaluates JavaScript template literals in bounded, isolated goja
// runtimes. Runtime slots are pooled for concurrency, while each slot is
// replaced after execution so user-created globals cannot cross requests.
type Engine struct {
	pool    *runtimePool
	timeout time.Duration
	// preview renders every execution-phase fact as its
	// placeholder; see Preview.
	preview bool
	// replay is the renderer semantics a frozen snapshot is replayed with
	// (1 = naming-render-1, 2 = naming-render-2, 3 = naming-render-3); 0
	// renders the current RendererVersion. See ForRenderer.
	replay int
}

// legacy reports whether the engine replays naming-render-1.
func (e *Engine) legacy() bool { return e.replay == 1 }

// Preview returns an engine for template previews: a fact whose
// value only exists when the file is processed (phase execution, e.g.
// media.resolution) renders as the placeholder ⟨path⟩
// (PreviewPlaceholder) and is never reported missing; everything else renders
// as usual. A placeholder marks where a value will appear — it is never a
// naming result.
func (e *Engine) Preview() *Engine {
	preview := *e
	preview.preview = true
	return &preview
}

// PreviewPlaceholder is the preview text of an execution-phase fact.
func PreviewPlaceholder(path string) string { return "⟨" + path + "⟩" }

// HasPreviewPlaceholder reports whether a preview render still holds a
// placeholder: the real path is decided only at execution, so nothing may
// compare it with a stored path.
func HasPreviewPlaceholder(rendered string) bool { return strings.Contains(rendered, "⟨") }

type runtimePool struct {
	slots chan *goja.Runtime
	size  int
}

func newRuntimePool(size int) *runtimePool {
	if size < 1 {
		size = 1
	}
	p := &runtimePool{slots: make(chan *goja.Runtime, size), size: size}
	for i := 0; i < size; i++ {
		p.slots <- newRuntime()
	}
	return p
}
func newRuntime() *goja.Runtime {
	rt := goja.New()
	// Date and eval are deliberately unavailable. Randomness is deterministic.
	_ = rt.Set("Date", nil)
	_ = rt.Set("eval", nil)
	_ = rt.Set("Math", map[string]interface{}{"random": func() float64 { return 0 }})
	for name, fn := range jsHelpers() {
		_ = rt.Set(name, fn)
	}
	installNamingValue(rt, false)
	return rt
}

// installNamingValue binds the interpolation guard. From naming-render-2 on an
// interpolated null/undefined renders "" (the rendered path is judged instead);
// naming-render-1 refuses it.
func installNamingValue(rt *goja.Runtime, strict bool) {
	_ = rt.Set("__namingValue", func(v goja.Value) goja.Value {
		if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
			if strict {
				panic(rt.NewTypeError("naming template value is missing; choose an explicit fallback"))
			}
			return rt.ToValue("")
		}
		return v
	})
}
func (p *runtimePool) acquire() *goja.Runtime  { return <-p.slots }
func (p *runtimePool) release(_ *goja.Runtime) { p.slots <- newRuntime() }

var (
	defaultEngine = &Engine{pool: newRuntimePool(4), timeout: DefaultTimeout}
	compileCache  sync.Map // map[string]*goja.Program
)

func NewEngine() *Engine { return defaultEngine }

// NewEngineWithLimits is useful for tests and composition roots with a
// deployment-specific execution budget.
func NewEngineWithLimits(poolSize int, timeout time.Duration) *Engine {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Engine{pool: newRuntimePool(poolSize), timeout: timeout}
}

type CompiledTemplate struct {
	Kind     TemplateKind
	Source   string
	Language string
	// RequiredFacts are the catalog fact paths this template reads outside any
	// conditional, sorted. A render that cannot supply one of them would emit a
	// name missing a fact the template asked for, so callers that re-render a
	// placed file from frozen evidence refuse instead.
	RequiredFacts []string
	// ReferencedFacts are every catalog fact path the template reads,
	// conditional or not, sorted.
	ReferencedFacts []string
	// strictFacts are the required facts read somewhere other than as a whole
	// interpolation (`${'S' + anime.season}`, `${pad(ep.number, 2)}`): an empty
	// value there would be coerced to "null" or thrown on rather than render
	// empty, so it refuses the render as NAMING_FACT_MISSING.
	strictFacts []string
	// leadingFact is the fact of a directory template that opens with the whole
	// interpolation `${fact}`: when it renders empty, the root of the rendered
	// path would come from the text after it, so the render is refused.
	leadingFact string
	// verbatimRoot is the literal directory text a directory template opens
	// with, through its last "/" (`/library/` in `/library/${anime.title}`).
	// It is the owner's own path, not a rendered value: the current renderer
	// keeps it as written and normalizes only the segments after it.
	verbatimRoot string
	program      *goja.Program
}

type CompiledVariant struct{ Directory, VideoFilename CompiledTemplate }

// RequiredFacts is the union of the facts the variant's directory and filename
// templates read outside any conditional, sorted and deduplicated.
func (v CompiledVariant) RequiredFacts() []string {
	seen := map[string]bool{}
	for _, template := range []CompiledTemplate{v.Directory, v.VideoFilename} {
		for _, fact := range template.RequiredFacts {
			seen[fact] = true
		}
	}
	facts := make([]string, 0, len(seen))
	for fact := range seen {
		facts = append(facts, fact)
	}
	sort.Strings(facts)
	return facts
}

// ReferencesFact reports whether the variant's directory or filename template
// reads path anywhere, conditional or not.
func (v CompiledVariant) ReferencesFact(path string) bool {
	for _, template := range []CompiledTemplate{v.Directory, v.VideoFilename} {
		for _, fact := range template.ReferencedFacts {
			if fact == path {
				return true
			}
		}
	}
	return false
}

type CompiledNamingRules struct {
	TV, Movie, Unknown CompiledVariant
	SubtitleSuffix     *CompiledTemplate
}

func unsupportedLanguageError(field string, language string) *Error {
	return &Error{Code: ErrCodeTemplateCompileError, Field: field, Message: fmt.Sprintf("unsupported naming language %q; %q is accepted", language, Language)}
}

// Compile compiles one template in the naming language.
func (e *Engine) Compile(kind TemplateKind, source string) (CompiledTemplate, error) {
	return e.compile(kind, source, Language, nil)
}

// CompileWithVars compiles one template and validates its `vars` references
// against the caller-supplied snapshot. The engine never reads the process-wide
// snapshot itself: the owner of that lifecycle injects it, so validation cannot
// silently depend on global state.
func (e *Engine) CompileWithVars(kind TemplateKind, source string, vars map[string]map[string]string) (CompiledTemplate, error) {
	return e.compile(kind, source, Language, vars)
}

// isOneTemplateLiteral reports whether a wrapped body is exactly one untagged
// template literal. A backtick inside `${...}` opens a nested literal and is
// fine; one outside an interpolation ends the engine's literal early, which
// is how a value still written in the old full-literal form shows up.
func isOneTemplateLiteral(expression string) bool {
	program, err := parser.ParseFile(nil, "naming-expression", "("+expression+")", 0)
	if err != nil || len(program.Body) != 1 {
		return false
	}
	statement, ok := program.Body[0].(*ast.ExpressionStatement)
	if !ok {
		return false
	}
	literal, ok := statement.Expression.(*ast.TemplateLiteral)
	return ok && literal.Tag == nil
}

func (e *Engine) compile(kind TemplateKind, source, language string, vars map[string]map[string]string) (CompiledTemplate, error) {
	if !SupportedLanguage(language) {
		return CompiledTemplate{}, unsupportedLanguageError(string(kind), language)
	}
	profile := languageProfileOf()
	if len(source) > MaxTemplateBytes {
		return CompiledTemplate{}, &Error{Code: ErrCodeTemplateLimitExceeded, Field: string(kind), Message: fmt.Sprintf("template exceeds the %d byte limit", MaxTemplateBytes)}
	}
	if strings.TrimSpace(source) == "" {
		return CompiledTemplate{Kind: kind, Source: source, Language: language}, nil
	}
	// The value is the body of one JavaScript template literal: the text as it
	// reads inside the backticks, with `${...}` interpolations. The server
	// supplies the enclosing backticks, so an owner never writes them and
	// parentheses or quotes in a folder name are plain text. The body is still
	// JavaScript source, so a literal backslash is written `\\` and a literal
	// backtick `\``; a bare backtick outside `${...}` is refused rather than
	// ending the literal.
	expression := "`" + source + "`"
	if strings.Contains(source, "`") && !isOneTemplateLiteral(expression) {
		return CompiledTemplate{}, &Error{Code: ErrCodeTemplateCompileError, Field: string(kind), Message: "the template is the text between the backticks: do not wrap it in backticks, and write a literal backtick as \\`"}
	}
	// Validation runs on every compile, cached program or not: the program cache
	// is keyed by source, while the `vars` snapshot is a separate input.
	if err := validateTemplateLimits(source, kind); err != nil {
		return CompiledTemplate{}, err
	}
	// The shorthand layer is a pure compile-time pre-pass: ${s00e00} becomes the
	// equivalent expression before anything analyses or runs it, so the required
	// facts, the guard and the compiled program all describe the expansion. See
	// shorthand.go for the recognition rule and the season semantics.
	expression = expandShorthands(expression)
	analyzed, err := analyzeNamingExpression(profile, expression, kind, vars)
	if err != nil {
		return CompiledTemplate{}, err
	}
	shapeExpression := expression
	expression, err = guardedInterpolations(expression, analyzed.interpolations)
	if err != nil {
		return CompiledTemplate{}, &Error{Code: ErrCodeTemplateCompileError, Field: string(kind), Message: err.Error()}
	}
	required := analyzed.required
	key := fmt.Sprintf("%s:%x", language, sha256.Sum256([]byte(source)))
	var program *goja.Program
	if cached, ok := compileCache.Load(key); ok {
		program = cached.(*goja.Program)
	} else {
		wrapped := `(function(__ctx) { const context=__ctx; ` + profile.bindings + `; return (` + expression + `); })`
		compiled, err := goja.Compile("naming-template", wrapped, false)
		if err != nil {
			return CompiledTemplate{}, &Error{Code: ErrCodeTemplateCompileError, Field: string(kind), Message: err.Error()}
		}
		program = compiled
		actual, _ := compileCache.LoadOrStore(key, program)
		program = actual.(*goja.Program)
	}
	requiredFacts := make([]string, 0, len(required))
	for fact := range required {
		requiredFacts = append(requiredFacts, fact)
	}
	sort.Strings(requiredFacts)
	referencedFacts := make([]string, 0, len(analyzed.referenced))
	for fact := range analyzed.referenced {
		referencedFacts = append(referencedFacts, fact)
	}
	sort.Strings(referencedFacts)
	strictFacts, leadingFact := interpolationShape(shapeExpression, analyzed.interpolations, requiredFacts)
	verbatimRoot := ""
	if kind == KindDirectory {
		if cut := strings.LastIndex(analyzed.literalHead, "/"); cut >= 0 {
			verbatimRoot = analyzed.literalHead[:cut+1]
		}
	}
	return CompiledTemplate{Kind: kind, Source: source, Language: language, RequiredFacts: requiredFacts, ReferencedFacts: referencedFacts, strictFacts: strictFacts, leadingFact: leadingFact, verbatimRoot: verbatimRoot, program: program}, nil
}

func (e *Engine) CompileRules(rules NamingRules) (CompiledNamingRules, error) {
	return e.compileRules(rules, nil)
}

// CompileRulesWithVars compiles a complete rules set and validates every
// template's `vars` references against the injected snapshot. Configuration
// saves use this form so an unsaveable template is refused at save time.
func (e *Engine) CompileRulesWithVars(rules NamingRules, vars map[string]map[string]string) (CompiledNamingRules, error) {
	return e.compileRules(rules, vars)
}

func (e *Engine) compileRules(rules NamingRules, vars map[string]map[string]string) (CompiledNamingRules, error) {
	if !SupportedLanguage(rules.Language) {
		return CompiledNamingRules{}, unsupportedLanguageError("rules", rules.Language)
	}

	compileVariant := func(v NamingVariant, field string) (CompiledVariant, error) {
		d, err := e.compile(KindDirectory, v.DirectoryTemplate, rules.Language, vars)
		if err != nil {
			return CompiledVariant{}, fmt.Errorf("compile %s directory template: %w", field, err)
		}
		f, err := e.compile(KindVideoFilename, v.VideoFilenameTemplate, rules.Language, vars)
		if err != nil {
			return CompiledVariant{}, fmt.Errorf("compile %s video filename template: %w", field, err)
		}
		return CompiledVariant{Directory: d, VideoFilename: f}, nil
	}
	tv, err := compileVariant(rules.TV, "tv")
	if err != nil {
		return CompiledNamingRules{}, err
	}
	movie, err := compileVariant(rules.Movie, "movie")
	if err != nil {
		return CompiledNamingRules{}, err
	}
	unknown, err := compileVariant(rules.Unknown, "unknown")
	if err != nil {
		return CompiledNamingRules{}, err
	}
	result := CompiledNamingRules{TV: tv, Movie: movie, Unknown: unknown}
	if strings.TrimSpace(rules.SubtitleSuffixTemplate) != "" {
		s, err := e.compile(KindSubtitleSuffix, rules.SubtitleSuffixTemplate, rules.Language, vars)
		if err != nil {
			return result, fmt.Errorf("compile subtitle suffix template: %w", err)
		}
		result.SubtitleSuffix = &s
	}
	return result, nil
}

// ValidateSavableRules is the save-time completeness check: every variant
// (tv, movie and unknown) must have a video filename template. An empty one
// would only fail later, when a file is rendered; the unknown variant gets no
// special case .
func ValidateSavableRules(rules NamingRules) error {
	for _, variant := range []struct {
		field string
		value NamingVariant
	}{{"tv", rules.TV}, {"movie", rules.Movie}, {"unknown", rules.Unknown}} {
		if strings.TrimSpace(variant.value.VideoFilenameTemplate) == "" {
			return &Error{Code: ErrCodeNamingTargetInvalid, Field: variant.field, Message: variant.field + " video filename template is empty"}
		}
	}
	return nil
}

// ValidateSavableRulesWithVars extends the completeness check with the target
// shape: each variant's directory template must render an absolute directory.
// It renders against the fixed catalog sample, so a syntactically valid but
// relative template is refused before it is stored. Save paths pass the loaded
// Vars snapshot; a missing ${vars...} reference is the caller's typed refusal.
func ValidateSavableRulesWithVars(rules NamingRules, vars map[string]map[string]string) error {
	if err := ValidateSavableRules(rules); err != nil {
		return err
	}
	sample, ok := ResolveSampleContext("sample-tv")
	if !ok {
		return errors.New("naming sample context is unavailable")
	}
	if vars != nil {
		withVars, err := WithVars(sample, vars)
		if err != nil {
			return err
		}
		sample = withVars
	}
	engine := NewEngine()
	for _, variant := range []struct {
		field string
		value NamingVariant
	}{{"tv", rules.TV}, {"movie", rules.Movie}, {"unknown", rules.Unknown}} {
		compiled, err := engine.CompileWithVars(KindDirectory, variant.value.DirectoryTemplate, vars)
		if err != nil {
			return err
		}
		// Render enforces the absolute-directory rule and returns the typed
		// NAMING_TARGET_NOT_ABSOLUTE refusal for relative or empty output.
		if _, err := engine.Render(compiled, sample); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) Render(t CompiledTemplate, ctx NamingContext) (string, error) {
	rendered, err := e.RenderDetailed(t, ctx)
	return rendered.Value, err
}

// Rendered is one template render: the normalized value and the nullable
// catalog facts the template read outside any fallback that had no value and
// rendered empty (sorted). EmptyFacts is what a preview lists and what a
// refusal names.
type Rendered struct {
	Value      string
	EmptyFacts []string
}

// RenderDetailed renders one template and reports the facts that rendered
// empty. A missing value alone never refuses the render: the normalized result
// is validated, and an invalid result names the empty facts (NAMING_FACT_MISSING
// with Missing) when there are any.
func (e *Engine) RenderDetailed(t CompiledTemplate, ctx NamingContext) (Rendered, error) {
	if !SupportedLanguage(t.Language) {
		return Rendered{}, unsupportedLanguageError(string(t.Kind), t.Language)
	}
	profile := languageProfileOf()
	if t.Source == "" {
		return Rendered{}, nil
	}
	facts := renderFacts(ctx, e.replay)
	empty := sortedMissing(profile.missing(t.RequiredFacts, &facts, e.preview))
	if e.legacy() && len(legacyRefused(empty)) > 0 {
		return Rendered{}, missingFactsError(t.Kind, legacyRefused(empty))
	}
	if undefined := undefinedFacts(empty, &facts); len(undefined) > 0 {
		return Rendered{}, undefinedFactsError(t.Kind, undefined)
	}
	if refused := strictEmptyFacts(t, empty); len(refused) > 0 {
		return Rendered{}, missingFactsError(t.Kind, refused)
	}
	value, err := e.execute(profile, t, &facts)
	if err != nil {
		return Rendered{}, withEmptyFacts(err, t.Kind, empty)
	}
	if !e.legacy() {
		value, err = normalizeRendered(t.Kind, value, t.verbatimRoot, e.replay == 2)
		if err != nil {
			return Rendered{}, withEmptyFacts(err, t.Kind, empty)
		}
	}
	if err := validateRendered(t.Kind, value); err != nil {
		return Rendered{}, withEmptyFacts(err, t.Kind, empty)
	}
	return Rendered{Value: value, EmptyFacts: empty}, nil
}

// normalizeRendered applies the path normalization of one template kind:
// naming-render-2's when render2 is set, the current one otherwise. The
// current renderer keeps a directory's verbatimRoot as written.
func normalizeRendered(kind TemplateKind, value, verbatimRoot string, render2 bool) (string, error) {
	switch kind {
	case KindDirectory:
		var normalized string
		var err error
		if !render2 && verbatimRoot != "" && strings.HasPrefix(value, verbatimRoot) {
			normalized, err = normalizeDirectoryAfterRoot(verbatimRoot, value[len(verbatimRoot):])
		} else {
			normalized, err = normalizeDirectory(value, render2)
		}
		if err == errEmptyDirectory {
			return "", &Error{Code: ErrCodeNamingTargetNotAbsolute, Field: string(kind), Message: err.Error()}
		}
		if err != nil {
			return "", &Error{Code: ErrCodeNamingTargetInvalid, Field: string(kind), Message: err.Error()}
		}
		return normalized, nil
	case KindVideoFilename:
		normalized, err := normalizeFilename(value, render2)
		if err != nil {
			return "", &Error{Code: ErrCodeNamingTargetInvalid, Field: string(kind), Message: err.Error()}
		}
		return normalized, nil
	}
	// A subtitle suffix is not normalized: it may legitimately render empty, and
	// a dot or separator left by an empty value is refused by its validation.
	return value, nil
}

func validateRendered(kind TemplateKind, value string) error {
	switch kind {
	case KindDirectory:
		if value == "" {
			return &Error{Code: ErrCodeNamingTargetNotAbsolute, Field: string(kind), Message: "rendered directory is empty; the target must be absolute"}
		}
		if err := validateRenderedTarget(value, true); err != nil {
			return &Error{Code: ErrCodeNamingTargetInvalid, Field: string(kind), Message: err.Error()}
		}
		if !filepath.IsAbs(value) {
			return &Error{Code: ErrCodeNamingTargetNotAbsolute, Field: string(kind), Message: "rendered directory must be absolute"}
		}
	case KindVideoFilename:
		if strings.TrimSpace(value) == "" {
			return &Error{Code: ErrCodeNamingTargetInvalid, Field: string(kind), Message: "rendered filename is empty"}
		}
		if err := validateRenderedTarget(value, false); err != nil {
			return &Error{Code: ErrCodeNamingTargetInvalid, Field: string(kind), Message: err.Error()}
		}
	case KindSubtitleSuffix:
		if err := validateSubtitleSuffix(value); err != nil {
			return &Error{Code: ErrCodeSubtitleSuffixInvalid, Field: string(kind), Message: err.Error()}
		}
	}
	return nil
}

// withEmptyFacts turns a render failure that facts without a value contributed
// to into a NAMING_FACT_MISSING refusal naming them; the original reason stays
// in the message. A failure with no empty fact is returned unchanged. Timeouts
// and limits are never attributed to a missing value.
func withEmptyFacts(err error, kind TemplateKind, empty []string) error {
	if len(empty) == 0 {
		return err
	}
	var namingErr *Error
	if !errors.As(err, &namingErr) {
		return err
	}
	switch namingErr.Code {
	case ErrCodeTemplateTimeout, ErrCodeTemplateLimitExceeded, ErrCodeNamingVarsUnknown, ErrCodeNamingFactUndefined:
		return err
	}
	return &Error{
		Code:    ErrCodeNamingFactMissing,
		Field:   string(kind),
		Message: fmt.Sprintf("%s; values without a value: %s", namingErr.Message, strings.Join(empty, ", ")),
		Missing: append([]string(nil), empty...),
	}
}

func (e *Engine) execute(profile *languageProfile, t CompiledTemplate, facts *templatefacts.FactSet) (string, error) {
	if t.program == nil {
		return "", nil
	}
	rt := e.pool.acquire()
	defer e.pool.release(rt)
	if e.legacy() {
		installNamingValue(rt, true)
	}
	plain, err := profile.object(facts, e.preview)
	if err != nil {
		return "", &Error{Code: ErrCodeTemplateCompileError, Field: string(t.Kind), Message: err.Error()}
	}
	programValue, runErr := rt.RunProgram(t.program)
	if runErr != nil {
		return "", &Error{Code: ErrCodeTemplateCompileError, Field: string(t.Kind), Message: runErr.Error()}
	}
	callable, ok := goja.AssertFunction(programValue)
	if !ok {
		return "", &Error{Code: ErrCodeTemplateCompileError, Field: string(t.Kind), Message: "template is not executable"}
	}
	var timer *time.Timer
	done := make(chan struct{})
	defer close(done)
	contextValue, ok := rt.ToValue(plain).(*goja.Object)
	if !ok {
		return "", &Error{Code: ErrCodeTemplateCompileError, Field: string(t.Kind), Message: "naming context is not an object"}
	}
	// Declared-only access: an unknown field throws here instead of rendering the
	// literal string "undefined" into a target path.
	guardContext(rt, contextValue, profile)
	// The budget is for the template, so it starts only now: building the
	// context object and installing its access guards is engine work sized by
	// the declared fields, and charging it to the template budget made a
	// loaded host report TEMPLATE_TIMEOUT for a template that never ran.
	timer = time.AfterFunc(e.timeout, func() {
		select {
		case <-done:
		default:
			rt.Interrupt(errors.New("naming template execution timed out"))
		}
	})
	defer timer.Stop()
	value, callErr := callable(goja.Undefined(), contextValue)
	if callErr != nil {
		return "", executionError(t.Kind, callErr)
	}
	if value == nil || goja.IsUndefined(value) || goja.IsNull(value) {
		return "", &Error{Code: ErrCodeNamingTargetInvalid, Field: string(t.Kind), Message: "template result is missing; choose an explicit fallback"}
	}
	text, err := jsString(value)
	if err != nil {
		return "", &Error{Code: ErrCodeTemplateLimitExceeded, Field: string(t.Kind), Message: err.Error()}
	}
	return text, nil
}

// executionError maps one goja execution failure onto the engine's typed errors.
func executionError(kind TemplateKind, callErr error) error {
	message := callErr.Error()
	lowered := strings.ToLower(message)
	if strings.Contains(lowered, "interrupt") || strings.Contains(lowered, "timed out") {
		return &Error{Code: ErrCodeTemplateTimeout, Field: string(kind), Message: "template execution exceeded timeout"}
	}
	if strings.Contains(lowered, "vars") && (strings.Contains(lowered, "undefined") || strings.Contains(lowered, "unknown")) {
		return &Error{Code: ErrCodeNamingVarsUnknown, Field: string(kind), Message: message}
	}
	if strings.Contains(message, "exceeds the") {
		return &Error{Code: ErrCodeTemplateLimitExceeded, Field: string(kind), Message: message}
	}
	return &Error{Code: ErrCodeTemplateCompileError, Field: string(kind), Message: message}
}

// guardRoot returns a proxy that serves only the properties the object already
// declares and throws for every other name.
func guardRoot(rt *goja.Runtime, object *goja.Object, label string) goja.Value {
	declared := make(map[string]struct{})
	for _, key := range object.Keys() {
		declared[key] = struct{}{}
	}
	return rt.ToValue(rt.NewProxy(object, &goja.ProxyTrapConfig{
		Get: func(target *goja.Object, property string, _ goja.Value) goja.Value {
			if _, ok := declared[property]; ok {
				return target.Get(property)
			}
			panic(rt.NewTypeError("unknown context field " + label + "." + property))
		},
	}))
}

// guardContext replaces every object-valued context root, and every group inside
// the Vars root, with a declared-only proxy. Anything else, and any property
// these objects do not declare, must fail loudly: a computed access like
// `Anime['Bo'+'gus']` used to slip past the source-text whitelist and render the
// literal string "undefined" into a target path. It runs per render on the
// freshly built context, so nothing from a previous render survives.
func guardContext(rt *goja.Runtime, context *goja.Object, profile *languageProfile) {
	for _, name := range profile.guardedRoots() {
		value := context.Get(name)
		object, ok := value.(*goja.Object)
		if !ok || goja.IsUndefined(value) || goja.IsNull(value) {
			continue
		}
		if name == profile.varsRoot {
			for _, group := range object.Keys() {
				if inner, ok := object.Get(group).(*goja.Object); ok {
					_ = object.Set(group, guardRoot(rt, inner, name+"."+group))
				}
			}
		}
		_ = context.Set(name, guardRoot(rt, object, name))
	}
}

// VideoRender is a rendered video target: the normalized directory and
// filename and the facts that rendered empty in either (sorted, deduplicated).
type VideoRender struct {
	Directory  string
	Filename   string
	EmptyFacts []string
}

func (v CompiledVariant) render(e *Engine, ctx NamingContext) (VideoRender, error) {
	directory, err := e.RenderDetailed(v.Directory, ctx)
	if err != nil {
		return VideoRender{}, err
	}
	filename, err := e.RenderDetailed(v.VideoFilename, ctx)
	if err != nil {
		return VideoRender{}, err
	}
	return VideoRender{Directory: directory.Value, Filename: filename.Value, EmptyFacts: MergeFacts(directory.EmptyFacts, filename.EmptyFacts)}, nil
}

// MergeFacts returns the sorted union of fact path lists.
func MergeFacts(lists ...[]string) []string {
	seen := map[string]bool{}
	for _, list := range lists {
		for _, fact := range list {
			seen[fact] = true
		}
	}
	if len(seen) == 0 {
		return nil
	}
	merged := make([]string, 0, len(seen))
	for fact := range seen {
		merged = append(merged, fact)
	}
	sort.Strings(merged)
	return merged
}

func (r CompiledNamingRules) RenderUnknown(e *Engine, ctx NamingContext) (string, string, error) {
	rendered, err := r.RenderUnknownDetailed(e, ctx)
	return rendered.Directory, rendered.Filename, err
}

// RenderUnknownDetailed renders the unknown variant and reports empty facts.
func (r CompiledNamingRules) RenderUnknownDetailed(e *Engine, ctx NamingContext) (VideoRender, error) {
	return r.Unknown.render(e, ctx)
}

func (r CompiledNamingRules) RenderVideo(e *Engine, ctx NamingContext) (directory, filename string, err error) {
	rendered, err := r.RenderVideoDetailed(e, ctx)
	return rendered.Directory, rendered.Filename, err
}

// RenderVideoDetailed renders the TV or movie variant (by the effective media
// kind) and reports empty facts.
func (r CompiledNamingRules) RenderVideoDetailed(e *Engine, ctx NamingContext) (VideoRender, error) {
	v := r.TV
	if MediaKind(ctx.Derived.EffectiveMediaKind) == MediaKindMovie {
		v = r.Movie
	}
	return v.render(e, ctx)
}

func (r CompiledNamingRules) RenderSubtitleSuffix(e *Engine, ctx NamingContext) (string, error) {
	rendered, err := r.RenderSubtitleSuffixDetailed(e, ctx)
	return rendered.Value, err
}

// RenderSubtitleSuffixDetailed renders the subtitle suffix and reports empty
// facts.
func (r CompiledNamingRules) RenderSubtitleSuffixDetailed(e *Engine, ctx NamingContext) (Rendered, error) {
	if r.SubtitleSuffix == nil {
		return Rendered{}, nil
	}
	return e.RenderDetailed(*r.SubtitleSuffix, ctx)
}

// CompanionSidecarFilename composes the filename of a subtitle that follows a
// video of the same release: the video's own basename (its filename without
// the extension), the rendered subtitle suffix when the rules render one, and
// the subtitle's own extension.
//
// It is the single copy of that rule. Placement
// (internal/fileoperation/application) and the RSS file preview both call it,
// so for the same inputs a previewed target and a frozen target are the same
// string — including the suffix, which the default rules render from the
// subtitle's own language (sub.lang_tag: its filename tag first, else the
// release title) and which a template may render empty.
//
// videoBasename is expected without the video's extension, subtitleExtension
// without its leading dot.
func CompanionSidecarFilename(videoBasename, suffix, subtitleExtension string) string {
	filename := videoBasename
	if suffix != "" {
		filename += "." + suffix
	}
	return filename + "." + subtitleExtension
}

func validateSubtitleSuffix(value string) error {
	if value == "" {
		return nil
	}
	if strings.IndexByte(value, 0) >= 0 {
		return fmt.Errorf("subtitle suffix contains NUL")
	}
	if strings.ContainsAny(value, `/\\`) {
		return fmt.Errorf("subtitle suffix must not contain a directory separator")
	}
	if filepath.IsAbs(value) || strings.HasPrefix(value, "~") {
		return fmt.Errorf("subtitle suffix must be relative")
	}
	if value == ".." || strings.HasPrefix(value, ".") {
		return fmt.Errorf("subtitle suffix must not start with a dot segment")
	}
	if len(value) > maxSubtitleSuffixBytes {
		return fmt.Errorf("subtitle suffix exceeds the %d byte limit", maxSubtitleSuffixBytes)
	}
	return nil
}
