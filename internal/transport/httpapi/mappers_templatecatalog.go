package httpapi

import (
	templatecatalogapplication "bansoko/internal/templatecatalog/application"
	"bansoko/internal/transport/httpapi/generated"
)

func mapNotificationTemplateCatalog(catalog templatecatalogapplication.NotificationTemplateCatalog) generated.NotificationTemplateCatalog {
	return generated.NotificationTemplateCatalog{
		CatalogVersion: generated.NotificationTemplateCatalogCatalogVersion(catalog.CatalogVersion),
		DefaultTemplate: generated.NotificationEventTemplateCustom{
			BodyTemplate:  catalog.DefaultTemplate.BodyTemplate,
			Mode:          generated.NotificationEventTemplateCustomMode(catalog.DefaultTemplate.Mode),
			TitleTemplate: catalog.DefaultTemplate.TitleTemplate,
		},
		Functions:       mapTemplateFunctions(catalog.Functions),
		LanguageVersion: generated.NotificationTemplateCatalogLanguageVersion(catalog.LanguageVersion),
		Sample:          mapTemplateSampleFacts(catalog.Sample),
		TriggerKind:     generated.NotificationTriggerKind(catalog.TriggerKind),
		Variables:       mapTemplateVariables(catalog.Variables),
	}
}

func mapNotificationTemplatePreview(preview templatecatalogapplication.NotificationTemplatePreview) generated.NotificationTemplatePreview {
	return generated.NotificationTemplatePreview{
		Body:            preview.Body,
		CatalogVersion:  generated.NotificationTemplatePreviewCatalogVersion(preview.CatalogVersion),
		Errors:          mapTemplateSourceErrors(preview.Errors),
		LanguageVersion: generated.NotificationTemplatePreviewLanguageVersion(preview.LanguageVersion),
		Title:           preview.Title,
		Valid:           preview.Valid,
		Variables:       mapTemplateVariables(preview.Variables),
		Warnings:        mapTemplateWarnings(preview.Warnings),
	}
}

func mapNotificationTemplatePreviewInput(input generated.NotificationTemplatePreviewInput) templatecatalogapplication.NotificationTemplatePreviewInput {
	return templatecatalogapplication.NotificationTemplatePreviewInput{
		BodyTemplate:    input.BodyTemplate,
		CatalogVersion:  templatecatalogapplication.NotificationTemplatePreviewInputCatalogVersion(input.CatalogVersion),
		LanguageVersion: templatecatalogapplication.NotificationTemplatePreviewInputLanguageVersion(input.LanguageVersion),
		TitleTemplate:   input.TitleTemplate,
		TriggerKind:     templatecatalogapplication.NotificationTriggerKind(input.TriggerKind),
	}
}

func mapNamingTemplateCatalog(catalog templatecatalogapplication.NamingTemplateCatalog) generated.NamingTemplateCatalog {
	fields := make([]generated.NamingCatalogField, 0, len(catalog.Fields))
	for _, field := range catalog.Fields {
		fields = append(fields, generated.NamingCatalogField{
			Description:    copyString(field.Description),
			Label:          copyString(field.Label),
			Nullable:       field.Nullable,
			Path:           field.Path,
			SampleValue:    copyString(field.SampleValue),
			Source:         field.Source,
			Type:           generated.NamingCatalogFieldType(field.Type),
			Phase:          templateFactPhase(field.Phase),
			LabelKey:       copyString(field.LabelKey),
			DescriptionKey: copyString(field.DescriptionKey),
		})
	}
	functions := make([]generated.NamingCatalogFunction, 0, len(catalog.Functions))
	for _, function := range catalog.Functions {
		functions = append(functions, generated.NamingCatalogFunction{
			Arguments:   cloneStrings(function.Arguments),
			Description: function.Description,
			Example:     copyString(function.Example),
			Name:        string(function.Name),
			Signature:   function.Signature,
		})
	}
	shorthands := make([]generated.NamingCatalogShorthand, 0, len(catalog.Shorthands))
	for _, shorthand := range catalog.Shorthands {
		shorthands = append(shorthands, generated.NamingCatalogShorthand{
			Name:      shorthand.Name,
			Expansion: shorthand.Expansion,
			Example:   shorthand.Example,
		})
	}
	samples := make([]generated.NamingCatalogSample, 0, len(catalog.Samples))
	for _, sample := range catalog.Samples {
		samples = append(samples, generated.NamingCatalogSample{
			Description: copyString(sample.Description),
			Label:       sample.Label,
			Ref:         sample.Ref,
		})
	}
	// vars_keys is a required, non-nullable array in the schema: normalize an
	// empty key set to [] rather than dropping the field into a null JSON value.
	varsKeys := make([]string, 0, len(catalog.VarsKeys))
	varsKeys = append(varsKeys, catalog.VarsKeys...)
	return generated.NamingTemplateCatalog{
		Language:     generated.NamingTemplateCatalogLanguage(catalog.Language),
		Fields:       fields,
		Functions:    functions,
		Shorthands:   shorthands,
		Samples:      samples,
		VarsKeys:     varsKeys,
		VarsLoadedAt: cloneTime(catalog.VarsLoadedAt),
	}
}

func mapNamingTemplatePreview(preview templatecatalogapplication.NamingTemplatePreview) generated.NamingTemplatePreview {
	var resolvedValues *[]generated.NamingResolvedValue
	if preview.ResolvedValues != nil {
		mapped := make([]generated.NamingResolvedValue, 0, len(*preview.ResolvedValues))
		for _, value := range *preview.ResolvedValues {
			mapped = append(mapped, generated.NamingResolvedValue{
				Evidence: copyString(value.Evidence),
				Key:      value.Key,
				Source:   copyString(value.Source),
				State:    generated.NamingResolvedValueState(value.State),
				Value:    copyString(value.Value),
			})
		}
		resolvedValues = &mapped
	}
	var emptyFacts *[]string
	if len(preview.EmptyFacts) > 0 {
		facts := append([]string(nil), preview.EmptyFacts...)
		emptyFacts = &facts
	}
	return generated.NamingTemplatePreview{
		ComposedTarget:  copyString(preview.ComposedTarget),
		EmptyFacts:      emptyFacts,
		Errors:          mapTemplateSourceErrors(preview.Errors),
		LockedBasename:  copyString(preview.LockedBasename),
		LockedDirectory: copyString(preview.LockedDirectory),
		LockedExtension: copyString(preview.LockedExtension),
		Rendered:        preview.Rendered,
		ResolvedValues:  resolvedValues,
		Valid:           preview.Valid,
		Warnings:        mapTemplateWarnings(preview.Warnings),
	}
}

func mapNamingTemplatePreviewInput(input generated.NamingTemplatePreviewInput) templatecatalogapplication.NamingTemplatePreviewInput {
	var language *string
	if input.Language != nil {
		value := string(*input.Language)
		language = &value
	}
	return templatecatalogapplication.NamingTemplatePreviewInput{
		Language:       language,
		SampleRef:      copyString(input.SampleRef),
		TemplateKind:   templatecatalogapplication.NamingTemplatePreviewInputTemplateKind(input.TemplateKind),
		TemplateSource: input.TemplateSource,
	}
}

func mapTemplateVariables(variables []templatecatalogapplication.TemplateVariable) []generated.TemplateVariable {
	mapped := make([]generated.TemplateVariable, 0, len(variables))
	for _, variable := range variables {
		mapped = append(mapped, generated.TemplateVariable{
			Description:    copyString(variable.Description),
			Label:          variable.Label,
			Name:           variable.Name,
			Nullable:       variable.Nullable,
			SampleValue:    copyString(variable.SampleValue),
			Source:         variable.Source,
			Type:           generated.TemplateVariableType(variable.Type),
			Phase:          templateFactPhase(variable.Phase),
			LabelKey:       copyString(variable.LabelKey),
			DescriptionKey: copyString(variable.DescriptionKey),
		})
	}
	return mapped
}

func templateFactPhase(phase *string) *generated.TemplateFactPhase {
	if phase == nil {
		return nil
	}
	value := generated.TemplateFactPhase(*phase)
	return &value
}

func mapTemplateFunctions(functions []templatecatalogapplication.TemplateFunction) []generated.TemplateFunction {
	mapped := make([]generated.TemplateFunction, 0, len(functions))
	for _, function := range functions {
		mapped = append(mapped, generated.TemplateFunction{
			Arguments:   cloneStrings(function.Arguments),
			Description: function.Description,
			Name:        generated.TemplateFunctionName(function.Name),
		})
	}
	return mapped
}

func mapTemplateSampleFacts(sample templatecatalogapplication.TemplateSampleFacts) generated.TemplateSampleFacts {
	return generated.TemplateSampleFacts{
		Provenance: cloneMapPointer(sample.Provenance),
		Values:     cloneMap(sample.Values),
	}
}

func mapTemplateSourceErrors(errors []templatecatalogapplication.TemplateSourceError) []generated.TemplateSourceError {
	mapped := make([]generated.TemplateSourceError, 0, len(errors))
	for _, sourceError := range errors {
		var field *generated.TemplateSourceErrorField
		if sourceError.Field != nil {
			mappedField := generated.TemplateSourceErrorField(*sourceError.Field)
			field = &mappedField
		}
		mapped = append(mapped, generated.TemplateSourceError{
			Code:    sourceError.Code,
			End:     sourceError.End,
			Field:   field,
			Message: sourceError.Message,
			Start:   sourceError.Start,
		})
	}
	return mapped
}

func mapTemplateWarnings(warnings []templatecatalogapplication.TemplateWarning) []generated.TemplateWarning {
	mapped := make([]generated.TemplateWarning, 0, len(warnings))
	for _, warning := range warnings {
		mapped = append(mapped, generated.TemplateWarning{
			Code:     warning.Code,
			Message:  warning.Message,
			Variable: copyString(warning.Variable),
		})
	}
	return mapped
}

func copyString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	cloned := make([]string, len(values))
	copy(cloned, values)
	return cloned
}

func cloneMap(values map[string]interface{}) map[string]interface{} {
	if values == nil {
		return nil
	}
	cloned := make(map[string]interface{}, len(values))
	for key, value := range values {
		cloned[key] = cloneValue(value)
	}
	return cloned
}

func cloneMapPointer(values *map[string]interface{}) *map[string]interface{} {
	if values == nil {
		return nil
	}
	cloned := cloneMap(*values)
	return &cloned
}

func cloneValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return cloneMap(typed)
	case []interface{}:
		cloned := make([]interface{}, len(typed))
		for index, item := range typed {
			cloned[index] = cloneValue(item)
		}
		return cloned
	default:
		return value
	}
}
