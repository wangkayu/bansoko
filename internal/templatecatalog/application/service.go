// Package application implements the template catalog and preview
// boundary. It serves the closed notification and naming variable/function
// catalogs plus a side-effect-free preview renderer that shares the
// bansoko-template-1 expression core. Preview never reads secrets, writes to the
// database, or calls an adapter.
package application

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"bansoko/internal/apperrors"
	"bansoko/internal/namingvars"
	"bansoko/internal/postprocessing/naming"
	"bansoko/internal/templatecatalog/notification"
)

// Service is the side-effect-free template catalog and preview boundary.
type Service struct{}

// NewService constructs the template catalog service.
func NewService() *Service { return &Service{} }

// ---------------------------------------------------------------------------
// Notification catalog
// ---------------------------------------------------------------------------

// GetNotificationCatalog returns the closed variable/function catalog, system
// default title/body templates, and fixed redacted sample facts for one trigger.
// catalogVersion must be empty or the current catalog version.
func (s *Service) GetNotificationCatalog(_ context.Context, triggerKind, catalogVersion string) (NotificationTemplateCatalog, error) {
	catalog, err := notificationCatalogVersion(catalogVersion)
	if err != nil {
		return NotificationTemplateCatalog{}, err
	}
	trigger := NotificationTriggerKind(triggerKind)
	variables, ok := notificationVariables(trigger)
	if !ok {
		return NotificationTemplateCatalog{}, &apperrors.BadRequest{Code: "TEMPLATE_TRIGGER_INVALID", Message: fmt.Sprintf("Unknown notification trigger kind %q.", triggerKind)}
	}
	defaultTitle, defaultBody := notification.Defaults(triggerKind)
	return NotificationTemplateCatalog{
		CatalogVersion:  NotificationTemplateCatalogCatalogVersion(catalog),
		DefaultTemplate: NotificationEventTemplateCustom{BodyTemplate: defaultBody, Mode: NotificationEventTemplateCustomModeCustom, TitleTemplate: defaultTitle},
		Functions:       catalogFunctions(),
		LanguageVersion: NotificationTemplateCatalogLanguageVersionBansokoTemplate1,
		Sample:          TemplateSampleFacts{Values: notification.Sample(triggerKind)},
		TriggerKind:     trigger,
		Variables:       variables,
	}, nil
}

// GetNamingCatalog returns the generated catalog of the naming language
// (empty selects it): object field paths with nullability, phase and copy
// keys, JavaScript helper signatures, shorthand tokens, versions, and the
// fixed redacted samples. The document is generated from the tables the engine
// enforces so the editor help cannot drift from execution.
func (s *Service) GetNamingCatalog(_ context.Context, language string) (NamingTemplateCatalog, error) {
	if language == "" {
		language = naming.Language
	}
	if !naming.SupportedLanguage(language) {
		return NamingTemplateCatalog{}, &apperrors.BadRequest{Code: "TEMPLATE_LANGUAGE_UNSUPPORTED", Message: fmt.Sprintf("Unsupported naming language %q.", language)}
	}
	doc := naming.GenerateCatalogDocument()
	fields := make([]NamingCatalogField, 0, len(doc.Fields))
	for _, field := range doc.Fields {
		fieldType := NamingCatalogFieldTypeString
		switch field.Type {
		case "list":
			fieldType = NamingCatalogFieldTypeList
		case "int":
			fieldType = NamingCatalogFieldTypeInteger
		case "number":
			fieldType = NamingCatalogFieldTypeNumber
		case "bool":
			fieldType = NamingCatalogFieldTypeBoolean
		}
		fields = append(fields, NamingCatalogField{
			Description:    strOrNil(field.Description),
			Nullable:       field.Nullable,
			Path:           field.Path,
			SampleValue:    strOrNil(field.SampleValue),
			Source:         field.Source,
			Type:           fieldType,
			Phase:          strOrNil(field.Phase),
			LabelKey:       strOrNil(field.LabelKey),
			DescriptionKey: strOrNil(field.DescriptionKey),
		})
	}
	functions := make([]NamingCatalogFunction, 0, len(doc.Functions))
	for _, function := range doc.Functions {
		functions = append(functions, NamingCatalogFunction{
			Arguments:   function.Arguments,
			Description: function.Description,
			Example:     strOrNil(function.Example),
			Name:        NamingCatalogFunctionName(function.Name),
			Signature:   function.Signature,
		})
	}
	shorthands := make([]NamingCatalogShorthand, 0, len(doc.Shorthands))
	for _, shorthand := range doc.Shorthands {
		shorthands = append(shorthands, NamingCatalogShorthand{Name: shorthand.Name, Expansion: shorthand.Expansion, Example: shorthand.Example})
	}
	samples := make([]NamingCatalogSample, 0, len(doc.Samples))
	for _, sample := range doc.Samples {
		samples = append(samples, NamingCatalogSample{
			Description: strOrNil(sample.Label),
			Label:       sample.Label,
			Ref:         sample.Ref,
		})
	}
	vars, _, loadedAt, varsErr := namingvars.CurrentMetadata()
	varsKeys := make([]string, 0, len(vars))
	if varsErr == nil {
		for key := range vars {
			varsKeys = append(varsKeys, key)
		}
		sort.Strings(varsKeys)
	}
	var loadedAtValue *time.Time
	if varsErr == nil {
		utc := loadedAt.UTC()
		loadedAtValue = &utc
	}
	return NamingTemplateCatalog{Language: doc.Language, Fields: fields, Functions: functions, Shorthands: shorthands, Samples: samples, VarsKeys: varsKeys, VarsLoadedAt: loadedAtValue}, nil
}

// PreviewNotification renders the supplied title/body templates against the
// fixed redacted sample facts for the given trigger. It performs no secret
// read, database write, or adapter call.
func (s *Service) PreviewNotification(_ context.Context, input NotificationTemplatePreviewInput) (NotificationTemplatePreview, error) {
	if string(input.LanguageVersion) != notification.LanguageVersion {
		return NotificationTemplatePreview{}, &apperrors.BadRequest{Code: "TEMPLATE_LANGUAGE_UNSUPPORTED", Message: fmt.Sprintf("Unsupported template language version %q.", input.LanguageVersion)}
	}
	catalog := string(input.CatalogVersion)
	if !notification.SupportedCatalog(catalog) {
		return NotificationTemplatePreview{}, &apperrors.BadRequest{Code: "TEMPLATE_CATALOG_UNSUPPORTED", Message: fmt.Sprintf("Unsupported notification catalog version %q.", input.CatalogVersion)}
	}
	variables, ok := notificationVariables(input.TriggerKind)
	if !ok {
		return NotificationTemplatePreview{}, &apperrors.BadRequest{Code: "TEMPLATE_TRIGGER_INVALID", Message: fmt.Sprintf("Unknown notification trigger kind %q.", input.TriggerKind)}
	}
	trigger := string(input.TriggerKind)
	sample := notification.Sample(trigger)
	// vars.<group>.<name> preview with the active Vars snapshot, the same
	// values the naming preview renders with.
	if vars, _, err := namingvars.Current(); err == nil {
		for group, values := range vars {
			for name, value := range values {
				sample["vars."+group+"."+name] = value
			}
		}
	}
	message, _ := notification.RenderMessage(trigger, input.TitleTemplate, input.BodyTemplate, sample)
	return NotificationTemplatePreview{
		Body:            message.Body,
		CatalogVersion:  NotificationTemplatePreviewCatalogVersion(catalog),
		Errors:          mapSourceErrors(message.Errors),
		LanguageVersion: NotificationTemplatePreviewLanguageVersionBansokoTemplate1,
		Title:           message.Title,
		Valid:           message.Valid(),
		Variables:       variables,
		Warnings:        mapWarnings(message.Warnings),
	}, nil
}

// PreviewNaming compiles and renders the supplied template, in the input
// language (the naming language when unset), against a fixed redacted sample context using the execution
// engine. It performs no database write, adapter call, or metadata I/O.
func (s *Service) PreviewNaming(_ context.Context, input NamingTemplatePreviewInput) (NamingTemplatePreview, error) {
	var kind naming.TemplateKind
	switch input.TemplateKind {
	case NamingTemplatePreviewInputTemplateKindDirectory:
		kind = naming.KindDirectory
	case NamingTemplatePreviewInputTemplateKindVideoFilename:
		kind = naming.KindVideoFilename
	case NamingTemplatePreviewInputTemplateKindSubtitleSuffix:
		kind = naming.KindSubtitleSuffix
	default:
		return NamingTemplatePreview{}, &apperrors.BadRequest{Code: "TEMPLATE_KIND_INVALID", Message: fmt.Sprintf("Unsupported naming template kind %q.", input.TemplateKind)}
	}

	language := naming.Language
	if input.Language != nil && *input.Language != "" {
		language = *input.Language
	}
	if !naming.SupportedLanguage(language) {
		return NamingTemplatePreview{}, &apperrors.BadRequest{Code: "TEMPLATE_LANGUAGE_UNSUPPORTED", Message: fmt.Sprintf("Unsupported naming language %q.", language)}
	}
	doc := naming.GenerateCatalogDocument()
	sampleRef := "sample-tv"
	if input.SampleRef != nil && strings.TrimSpace(*input.SampleRef) != "" {
		sampleRef = strings.TrimSpace(*input.SampleRef)
	}
	var sampleContext naming.NamingContext
	var sampleLocked *naming.SampleLockedTarget
	found := false
	for _, sample := range doc.Samples {
		if sample.Ref == sampleRef {
			sampleContext = sample.Context
			sampleLocked = sample.Locked
			found = true
			break
		}
	}
	if !found {
		return NamingTemplatePreview{}, &apperrors.BadRequest{Code: "TEMPLATE_SAMPLE_UNKNOWN", Message: fmt.Sprintf("Unknown naming sample %q.", sampleRef)}
	}

	// Resolver parity: the sample's Parsed/Derived values come from the same
	// registry execution uses, and the per-resolver results (with evidence)
	// flow into the preview response.
	resolved := naming.DefaultRegistry().ResolveAll(naming.ResolverInput{Facts: sampleContext})
	sampleContext = resolved.Context
	// Execution injects the initialized Vars snapshot on every naming render path.
	// The preview must render the same context a real operation would, or a
	// template referencing ${vars...} would compile and then fail only here. When
	// the channel is not initialized the snapshot stays empty, and such a template
	// already fails at compile with the same error execution would report.
	if withVars, varsErr := naming.WithCurrentVars(sampleContext); varsErr == nil {
		sampleContext = withVars
	}

	resolvedValues := make([]NamingResolvedValue, 0, len(resolved.Results))
	for _, result := range resolved.Results {
		var source *string
		if len(result.Evidence) > 0 {
			source = strOrNil(result.Evidence[0].InputPath)
		}
		resolvedValues = append(resolvedValues, NamingResolvedValue{
			Evidence: strOrNil(naming.SummarizeResolverEvidence(result.Evidence)),
			Key:      result.Key,
			Source:   source,
			State:    NamingResolvedValueState(result.State),
			Value:    strOrNil(naming.SummarizeResolverValue(result)),
		})
	}

	preview := NamingTemplatePreview{
		Errors:         []TemplateSourceError{},
		ResolvedValues: &resolvedValues,
		Warnings:       []TemplateWarning{},
	}

	// Execution-phase facts preview as placeholders (⟨media.resolution⟩).
	engine := naming.NewEngine().Preview()
	compiled, err := engine.Compile(kind, input.TemplateSource)
	if err == nil {
		var rendered naming.Rendered
		rendered, err = engine.RenderDetailed(compiled, sampleContext)
		preview.Rendered, preview.EmptyFacts = rendered.Value, rendered.EmptyFacts
	}
	if err != nil {
		preview.Errors = append(preview.Errors, namingTemplateError(err))
		preview.Valid = false
		// A refusal that empty values caused names them like a valid render.
		var namingErr *naming.Error
		if errors.As(err, &namingErr) && len(namingErr.Missing) > 0 {
			preview.EmptyFacts = append([]string(nil), namingErr.Missing...)
		}
		return preview, nil
	}
	preview.Valid = true

	switch kind {
	case naming.KindVideoFilename:
		// Compose the previewed filename with the default-rules directory for
		// the same sample so the user sees the full relative target.
		if directory, _, composeErr := renderDefaultVideoTarget(engine, sampleContext); composeErr == nil {
			preview.ComposedTarget = strOrNil(composeTarget(directory, preview.Rendered))
		}
	case naming.KindSubtitleSuffix:
		// Echo the locked bound-video segments; the suffix can never replace
		// directory, basename, or extension. Subtitle samples carry their
		// frozen bound-video target as fixed sample data; video samples fall
		// back to rendering the sample through the default naming rules.
		var directory, basename, lockedExtension string
		lockedKnown := false
		if sampleLocked != nil {
			directory = sampleLocked.Directory
			basename = sampleLocked.Basename
			lockedExtension = sampleLocked.Extension
			lockedKnown = true
		} else if renderedDirectory, renderedFilename, composeErr := renderDefaultVideoTarget(engine, sampleContext); composeErr == nil {
			directory = renderedDirectory
			lockedExtension = sampleContext.File.Extension
			basename = strings.TrimSuffix(renderedFilename, "."+lockedExtension)
			lockedKnown = true
		}
		if lockedKnown {
			// The composed target keeps the subtitle file's own extension
			// while the locked extension echoes the bound video's.
			composedFilename := basename
			if preview.Rendered != "" {
				composedFilename += "." + preview.Rendered
			}
			if fileExtension := sampleContext.File.Extension; fileExtension != "" {
				composedFilename += "." + fileExtension
			}
			preview.LockedDirectory = strOrNil(directory)
			preview.LockedBasename = strOrNil(basename)
			preview.LockedExtension = strOrNil(lockedExtension)
			preview.ComposedTarget = strOrNil(composeTarget(directory, composedFilename))
		}
	}
	return preview, nil
}

// renderDefaultVideoTarget renders the sample video file through the default
// naming rules. It powers composed previews; a failure here never invalidates
// the user's own template result.
func renderDefaultVideoTarget(engine *naming.Engine, ctx naming.NamingContext) (directory, filename string, err error) {
	compiled, err := engine.CompileRules(naming.DefaultNamingRules())
	if err != nil {
		return "", "", err
	}
	return compiled.RenderVideo(engine, ctx)
}

func composeTarget(directory, filename string) string {
	if directory == "" {
		return filename
	}
	return directory + "/" + filename
}

// namingTemplateError converts an engine error into a bounded template source
// error. Non-typed failures are reported as compile errors.
func namingTemplateError(err error) TemplateSourceError {
	field := TemplateSourceErrorFieldTemplateSource
	sourceErr := TemplateSourceError{Code: naming.ErrCodeTemplateCompileError, Field: &field, Message: err.Error()}
	var namingErr *naming.Error
	if errors.As(err, &namingErr) {
		sourceErr.Code = namingErr.Code
		sourceErr.Message = namingErr.Message
	}
	return sourceErr
}

// ---------------------------------------------------------------------------
// Notification core projection
// ---------------------------------------------------------------------------

func catalogFunctions() []TemplateFunction {
	defs := notification.Functions()
	functions := make([]TemplateFunction, 0, len(defs))
	for _, def := range defs {
		functions = append(functions, TemplateFunction{
			Arguments:   def.Arguments,
			Description: def.Description,
			Name:        TemplateFunctionName(def.Name),
		})
	}
	return functions
}

// notificationVariables returns the closed variable subset for a trigger. The
// second return value reports whether the trigger kind is known.
func notificationVariables(trigger NotificationTriggerKind) ([]TemplateVariable, bool) {
	defs, ok := notification.Variables(string(trigger))
	if !ok {
		return nil, false
	}
	variables := make([]TemplateVariable, 0, len(defs))
	for _, def := range defs {
		variables = append(variables, TemplateVariable{
			Description:    strOrNil(def.Description),
			Label:          def.Label,
			Name:           def.Name,
			Nullable:       def.Nullable,
			SampleValue:    strOrNil(def.SampleValue),
			Source:         def.Source,
			Type:           TemplateVariableType(def.Type),
			Phase:          strOrNil(def.Phase),
			LabelKey:       strOrNil(def.LabelKey),
			DescriptionKey: strOrNil(def.DescriptionKey),
		})
	}
	return variables, true
}

func mapSourceErrors(errors []notification.SourceError) []TemplateSourceError {
	mapped := make([]TemplateSourceError, 0, len(errors))
	for _, item := range errors {
		field := TemplateSourceErrorField(item.Field)
		mapped = append(mapped, TemplateSourceError{Code: item.Code, End: item.End, Field: &field, Message: item.Message, Start: item.Start})
	}
	return mapped
}

func mapWarnings(warnings []notification.Warning) []TemplateWarning {
	mapped := make([]TemplateWarning, 0, len(warnings))
	for _, item := range warnings {
		warning := TemplateWarning{Code: item.Code, Message: item.Message}
		if item.Variable != "" {
			variable := item.Variable
			warning.Variable = &variable
		}
		mapped = append(mapped, warning)
	}
	return mapped
}

// notificationCatalogVersion resolves a requested catalog version; empty is
// the current catalog.
func notificationCatalogVersion(version string) (string, error) {
	if version == "" {
		return notification.CatalogVersion, nil
	}
	if !notification.SupportedCatalog(version) {
		return "", &apperrors.BadRequest{Code: "TEMPLATE_CATALOG_UNSUPPORTED", Message: fmt.Sprintf("Unsupported notification catalog version %q.", version)}
	}
	return version, nil
}

func strOrNil(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// RenderNotification renders title and body templates against the fixed
// redacted sample facts for the given trigger through the shared notification
// core. The delivery handler uses it for test deliveries, which freeze the
// template snapshot (and its catalog version) but no owner facts.
func (s *Service) RenderNotification(titleTemplate, bodyTemplate, triggerKind, catalogVersion string) (title, body string, err error) {
	if !notification.SupportedCatalog(catalogVersion) {
		return "", "", fmt.Errorf("unsupported notification catalog version %q", catalogVersion)
	}
	message, ok := notification.RenderMessage(triggerKind, titleTemplate, bodyTemplate, notification.Sample(triggerKind))
	if !ok {
		return "", "", fmt.Errorf("unknown notification trigger kind %q", triggerKind)
	}
	if !message.Valid() {
		first := message.Errors[0]
		return "", "", fmt.Errorf("%s template render error: %s", first.Field, first.Message)
	}
	return message.Title, message.Body, nil
}
