package httpapi

import (
	"bansoko/internal/notifypresent"
	templatecatalogapplication "bansoko/internal/templatecatalog/application"
	"bansoko/internal/templatecatalog/notification"
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
	attachments := generated.NotificationTemplatePreviewAttachments{
		Links:   []generated.NotificationTemplatePreviewLink{},
		Buttons: mapNotificationButtons(preview.Attachments.Buttons),
	}
	if preview.Attachments.Image != nil {
		attachments.Image = &generated.NotificationTemplatePreviewImage{
			Source: generated.NotificationTemplatePreviewImageSource(preview.Attachments.Image.Source),
			Url:    copyString(preview.Attachments.Image.URL),
		}
	}
	for _, link := range preview.Attachments.Links {
		attachments.Links = append(attachments.Links, generated.NotificationTemplatePreviewLink{
			Provider: generated.NotificationTemplatePreviewLinkProvider(link.Provider), Label: link.Label, Url: link.URL,
		})
	}
	bodyParts := make([]generated.NotificationTemplatePreviewBodyPart, 0, len(preview.BodyParts))
	for _, part := range preview.BodyParts {
		mapped := generated.NotificationTemplatePreviewBodyPart{Text: part.Text}
		if part.Link != nil {
			mapped.Link = &generated.NotificationTemplatePreviewLink{
				Provider: generated.NotificationTemplatePreviewLinkProvider(part.Link.Provider), Label: part.Link.Label, Url: part.Link.URL,
			}
		}
		bodyParts = append(bodyParts, mapped)
	}
	return generated.NotificationTemplatePreview{
		Attachments:     attachments,
		Body:            preview.Body,
		BodyParts:       bodyParts,
		CatalogVersion:  generated.NotificationTemplatePreviewCatalogVersion(preview.CatalogVersion),
		Errors:          mapTemplateSourceErrors(preview.Errors),
		LanguageVersion: generated.NotificationTemplatePreviewLanguageVersion(preview.LanguageVersion),
		Presentation:    mapNotificationTemplatePresentation(preview.Presentation),
		Title:           preview.Title,
		Valid:           preview.Valid,
		Variables:       mapTemplateVariables(preview.Variables),
		Warnings:        mapTemplateWarnings(preview.Warnings),
	}
}

func mapNotificationTemplatePresentation(presentation notifypresent.Presentation) generated.NotificationTemplatePreviewPresentation {
	telegramParts := make([]generated.NotificationTemplatePreviewBodyPart, 0, len(presentation.Telegram.Parts))
	for _, part := range presentation.Telegram.Parts {
		telegramParts = append(telegramParts, mapNotificationBodyPart(part.Text, part.Link))
	}
	feishuContent := make([][]generated.NotificationTemplatePreviewFeishuElement, 0, len(presentation.Feishu.Content))
	for _, paragraph := range presentation.Feishu.Content {
		elements := make([]generated.NotificationTemplatePreviewFeishuElement, 0, len(paragraph))
		for _, element := range paragraph {
			mapped := generated.NotificationTemplatePreviewFeishuElement{
				Tag:  generated.NotificationTemplatePreviewFeishuElementTag(element.Tag),
				Text: element.Text,
			}
			if element.Href != "" {
				href := element.Href
				mapped.Href = &href
			}
			elements = append(elements, mapped)
		}
		feishuContent = append(feishuContent, elements)
	}
	var feishuImage *generated.NotificationTemplatePreviewFeishuImage
	if presentation.Feishu.Image != nil {
		feishuImage = &generated.NotificationTemplatePreviewFeishuImage{
			Label: presentation.Feishu.Image.Label,
			Url:   presentation.Feishu.Image.URL,
		}
	}
	webhookLinks := make([]generated.NotificationTemplatePreviewLink, 0, len(presentation.Webhook.Message.Links))
	for _, link := range presentation.Webhook.Message.Links {
		webhookLinks = append(webhookLinks, generated.NotificationTemplatePreviewLink{
			Provider: generated.NotificationTemplatePreviewLinkProvider(link.Provider),
			Label:    link.Label,
			Url:      link.URL,
		})
	}
	var webhookImage *generated.NotificationTemplatePreviewWebhookImage
	if image := presentation.Webhook.Message.Image; image != nil {
		webhookImage = &generated.NotificationTemplatePreviewWebhookImage{Source: image.Source}
		if image.URL != "" {
			url := image.URL
			webhookImage.Url = &url
		}
		if image.Placeholder {
			placeholder := true
			webhookImage.Placeholder = &placeholder
		}
	}
	return generated.NotificationTemplatePreviewPresentation{
		Telegram: generated.NotificationTemplatePreviewTelegram{
			Mode:         generated.NotificationTemplatePreviewTelegramMode(presentation.Telegram.Mode),
			Text:         presentation.Telegram.Text,
			Title:        presentation.Telegram.Title,
			BodyParts:    telegramParts,
			Buttons:      mapNotificationButtons(presentation.Telegram.Buttons),
			TextUnits:    presentation.Telegram.TextUnits,
			ImageOmitted: presentation.Telegram.ImageOmitted,
			Truncated:    presentation.Telegram.Truncated,
		},
		Feishu: generated.NotificationTemplatePreviewFeishu{
			Title:        presentation.Feishu.Title,
			Content:      feishuContent,
			Image:        feishuImage,
			ImageOmitted: presentation.Feishu.ImageOmitted,
		},
		Webhook: generated.NotificationTemplatePreviewWebhook{
			Message: generated.NotificationTemplatePreviewWebhookMessage{
				Title: presentation.Webhook.Message.Title,
				Body:  presentation.Webhook.Message.Body,
				Image: webhookImage,
				Links: webhookLinks,
			},
		},
	}
}

func mapNotificationButtons(buttons []notification.Button) []generated.NotificationTemplatePreviewButton {
	mapped := make([]generated.NotificationTemplatePreviewButton, 0, len(buttons))
	for _, button := range buttons {
		mapped = append(mapped, generated.NotificationTemplatePreviewButton{
			Provider: generated.NotificationTemplatePreviewButtonProvider(button.Provider), Label: button.Label, Url: button.URL,
		})
	}
	return mapped
}

func mapNotificationBodyPart(text string, link *notification.Link) generated.NotificationTemplatePreviewBodyPart {
	part := generated.NotificationTemplatePreviewBodyPart{Text: text}
	if link != nil {
		part.Link = &generated.NotificationTemplatePreviewLink{Provider: generated.NotificationTemplatePreviewLinkProvider(link.Provider), Label: link.Label, Url: link.URL}
	}
	return part
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
	return generated.NamingTemplateCatalog{
		Language:   generated.NamingTemplateCatalogLanguage(catalog.Language),
		Fields:     fields,
		Functions:  functions,
		Shorthands: shorthands,
		Samples:    samples,
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
