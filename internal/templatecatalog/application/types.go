package application

import "time"

// NotificationTriggerKind identifies one supported notification event.
type NotificationTriggerKind string

const (
	NotificationTriggerKindDownloadSubmitted         NotificationTriggerKind = "download_submitted"
	NotificationTriggerKindFileOperationCompleted    NotificationTriggerKind = "file_operation_completed"
	NotificationTriggerKindFileOperationFailed       NotificationTriggerKind = "file_operation_failed"
	NotificationTriggerKindReleaseDiscovered         NotificationTriggerKind = "release_discovered"
	NotificationTriggerKindDownloadFailed            NotificationTriggerKind = "download_failed"
	NotificationTriggerKindSubscriptionChanged       NotificationTriggerKind = "subscription_changed"
	NotificationTriggerKindUnknownFilesImported      NotificationTriggerKind = "unknown_files_imported"
	NotificationTriggerKindReplacementDeletionFailed NotificationTriggerKind = "replacement_deletion_failed"
)

type TemplateVariableType string

const (
	TemplateVariableTypeBoolean   TemplateVariableType = "boolean"
	TemplateVariableTypeInteger   TemplateVariableType = "integer"
	TemplateVariableTypeNumber    TemplateVariableType = "number"
	TemplateVariableTypeString    TemplateVariableType = "string"
	TemplateVariableTypeTimestamp TemplateVariableType = "timestamp"
	TemplateVariableTypeList      TemplateVariableType = "list"
)

type TemplateSourceErrorField string

const (
	TemplateSourceErrorFieldBodyTemplate   TemplateSourceErrorField = "body_template"
	TemplateSourceErrorFieldTemplateSource TemplateSourceErrorField = "template_source"
	TemplateSourceErrorFieldTitleTemplate  TemplateSourceErrorField = "title_template"
)

type TemplateVariable struct {
	Description *string
	Label       string
	Name        string
	Nullable    bool
	SampleValue *string
	Source      string
	Type        TemplateVariableType
	// Phase is release or execution; LabelKey/DescriptionKey name the
	// generated copy.
	Phase          *string
	LabelKey       *string
	DescriptionKey *string
}

type TemplateFunctionName string

type TemplateFunction struct {
	Arguments   []string
	Description string
	Name        TemplateFunctionName
}

type TemplateSampleFacts struct {
	Provenance *map[string]interface{}
	Values     map[string]interface{}
}

type TemplateSourceError struct {
	Code    string
	End     int
	Field   *TemplateSourceErrorField
	Message string
	Start   int
}

type TemplateWarning struct {
	Code     string
	Message  string
	Variable *string
}

type NotificationEventTemplateCustomMode string

const NotificationEventTemplateCustomModeCustom NotificationEventTemplateCustomMode = "custom"

type NotificationEventTemplateCustom struct {
	BodyTemplate  string
	Mode          NotificationEventTemplateCustomMode
	TitleTemplate string
}

type NotificationTemplateCatalogCatalogVersion string

const NotificationTemplateCatalogCatalogVersionNotificationCatalogV1 NotificationTemplateCatalogCatalogVersion = "notification-catalog-v1"

type NotificationTemplateCatalogLanguageVersion string

const NotificationTemplateCatalogLanguageVersionBansokoTemplate1 NotificationTemplateCatalogLanguageVersion = "bansoko-template-1"

type NotificationTemplateCatalog struct {
	CatalogVersion  NotificationTemplateCatalogCatalogVersion
	DefaultTemplate NotificationEventTemplateCustom
	Functions       []TemplateFunction
	LanguageVersion NotificationTemplateCatalogLanguageVersion
	Sample          TemplateSampleFacts
	TriggerKind     NotificationTriggerKind
	Variables       []TemplateVariable
}

type NotificationTemplatePreviewCatalogVersion string

const NotificationTemplatePreviewCatalogVersionNotificationCatalogV1 NotificationTemplatePreviewCatalogVersion = "notification-catalog-v1"

type NotificationTemplatePreviewLanguageVersion string

const NotificationTemplatePreviewLanguageVersionBansokoTemplate1 NotificationTemplatePreviewLanguageVersion = "bansoko-template-1"

type NotificationTemplatePreview struct {
	Body            string
	CatalogVersion  NotificationTemplatePreviewCatalogVersion
	Errors          []TemplateSourceError
	LanguageVersion NotificationTemplatePreviewLanguageVersion
	Title           string
	Valid           bool
	Variables       []TemplateVariable
	Warnings        []TemplateWarning
}

type NotificationTemplatePreviewInputCatalogVersion string

const NotificationTemplatePreviewInputCatalogVersionNotificationCatalogV1 NotificationTemplatePreviewInputCatalogVersion = "notification-catalog-v1"

type NotificationTemplatePreviewInputLanguageVersion string

const NotificationTemplatePreviewInputLanguageVersionBansokoTemplate1 NotificationTemplatePreviewInputLanguageVersion = "bansoko-template-1"

type NotificationTemplatePreviewInput struct {
	BodyTemplate    string
	CatalogVersion  NotificationTemplatePreviewInputCatalogVersion
	LanguageVersion NotificationTemplatePreviewInputLanguageVersion
	TitleTemplate   string
	TriggerKind     NotificationTriggerKind
}

type NamingCatalogFieldType string

const (
	NamingCatalogFieldTypeBoolean   NamingCatalogFieldType = "boolean"
	NamingCatalogFieldTypeInteger   NamingCatalogFieldType = "integer"
	NamingCatalogFieldTypeList      NamingCatalogFieldType = "list"
	NamingCatalogFieldTypeNumber    NamingCatalogFieldType = "number"
	NamingCatalogFieldTypeString    NamingCatalogFieldType = "string"
	NamingCatalogFieldTypeTimestamp NamingCatalogFieldType = "timestamp"
)

type NamingCatalogField struct {
	Description *string
	Label       *string
	Nullable    bool
	Path        string
	SampleValue *string
	Source      string
	Type        NamingCatalogFieldType
	// Phase is release or execution; LabelKey/DescriptionKey name the
	// generated copy.
	Phase          *string
	LabelKey       *string
	DescriptionKey *string
}

type NamingCatalogFunctionName string

type NamingCatalogFunction struct {
	Arguments   []string
	Description string
	Example     *string
	Name        NamingCatalogFunctionName
	Signature   string
}

// NamingCatalogShorthand is one registered shorthand token, the js-naming-1
// expression it expands to, and what that expansion renders for the documented
// sample. The editor shows the token and the example; the expansion is the
// compiler's own table, never a second copy.
type NamingCatalogShorthand struct {
	Name      string
	Expansion string
	Example   string
}

type NamingCatalogSample struct {
	Description *string
	Label       string
	Ref         string
}

type NamingResolvedValueState string

const NamingResolvedValueStateValue NamingResolvedValueState = "value"

type NamingResolvedValue struct {
	Evidence *string
	Key      string
	Source   *string
	State    NamingResolvedValueState
	Value    *string
}
type NamingTemplateCatalog struct {
	Language     string
	Fields       []NamingCatalogField
	Functions    []NamingCatalogFunction
	Shorthands   []NamingCatalogShorthand
	Samples      []NamingCatalogSample
	VarsKeys     []string
	VarsLoadedAt *time.Time
}

type NamingTemplatePreview struct {
	ComposedTarget *string
	// EmptyFacts are the naming facts that had no value for the sample and
	// rendered empty in Rendered (sorted catalog paths).
	EmptyFacts      []string
	Errors          []TemplateSourceError
	LockedBasename  *string
	LockedDirectory *string
	LockedExtension *string
	Rendered        string
	ResolvedValues  *[]NamingResolvedValue
	Valid           bool
	Warnings        []TemplateWarning
}

type NamingTemplatePreviewInputTemplateKind string

const (
	NamingTemplatePreviewInputTemplateKindDirectory      NamingTemplatePreviewInputTemplateKind = "directory"
	NamingTemplatePreviewInputTemplateKindSubtitleSuffix NamingTemplatePreviewInputTemplateKind = "subtitle_suffix"
	NamingTemplatePreviewInputTemplateKindVideoFilename  NamingTemplatePreviewInputTemplateKind = "video_filename"
)

type NamingTemplatePreviewInput struct {
	// Language of TemplateSource; nil is the current language.
	Language       *string
	SampleRef      *string
	TemplateKind   NamingTemplatePreviewInputTemplateKind
	TemplateSource string
}
