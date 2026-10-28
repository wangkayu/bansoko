package application

import (
	"encoding/json"
	"time"

	plugintargetport "bansoko/internal/plugintarget/port"
)

// TriggerKind is the closed business notification trigger vocabulary.
type TriggerKind string

// These alias the port registry rather than restating its literals: the registry
// is the one declaration, so a trigger added there is immediately nameable here
// and cannot exist in two spellings. The template catalog keeps its own copy
// (it also owns each trigger's variables and defaults); a conformance test in
// plugintarget compares that copy to this registry as a closed set.
const (
	TriggerDownloadSubmitted         TriggerKind = plugintargetport.TriggerDownloadSubmitted
	TriggerFileOperationCompleted    TriggerKind = plugintargetport.TriggerFileOperationCompleted
	TriggerFileOperationFailed       TriggerKind = plugintargetport.TriggerFileOperationFailed
	TriggerReleaseDiscovered         TriggerKind = plugintargetport.TriggerReleaseDiscovered
	TriggerDownloadFailed            TriggerKind = plugintargetport.TriggerDownloadFailed
	TriggerSubscriptionChanged       TriggerKind = plugintargetport.TriggerSubscriptionChanged
	TriggerUnknownFilesImported      TriggerKind = plugintargetport.TriggerUnknownFilesImported
	TriggerReplacementDeletionFailed TriggerKind = plugintargetport.TriggerReplacementDeletionFailed
)

type PluginKind string
type AdapterKind string

const (
	PluginKindNotification PluginKind  = "notification"
	AdapterTelegram        AdapterKind = "telegram"
	AdapterFeishu          AdapterKind = "feishu"
	AdapterWebhook         AdapterKind = "webhook"
)

type SecretInput struct {
	Mode  string
	Value json.RawMessage
}

type TargetInput struct {
	AdapterKind         AdapterKind
	Config              json.RawMessage
	ConfigSchemaVersion int
	Enabled             bool
	Name                string
	PluginKind          PluginKind
	Secret              SecretInput
	TemplateSet         json.RawMessage
	TriggerKinds        []TriggerKind
}

type TargetUpdateInput struct {
	TargetInput
	ExpectedVersion int64
}

type TargetTestInput struct {
	ExpectedVersion int64
	TriggerKind     TriggerKind
}

type TargetConfig struct {
	AdapterKind         AdapterKind     `json:"adapter_kind"`
	Config              json.RawMessage `json:"config"`
	ConfigStatus        string          `json:"config_status"`
	ConfigSchemaVersion int             `json:"config_schema_version"`
	Enabled             bool            `json:"enabled"`
	HasSecret           *bool           `json:"has_secret,omitempty"`
	// HasSigningSecret is set for a webhook target only: whether its secret
	// bundle carries a signing secret. The secret itself is write-only.
	HasSigningSecret *bool           `json:"has_signing_secret,omitempty"`
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	PluginKind       PluginKind      `json:"plugin_kind"`
	TemplateSet      json.RawMessage `json:"template_set"`
	TriggerKinds     []TriggerKind   `json:"trigger_kinds"`
	UpdatedAt        time.Time       `json:"updated_at"`
	Version          int64           `json:"version"`
}

type TargetPage struct {
	Targets []TargetConfig `json:"targets"`
}

type TestAccepted struct {
	Accepted   bool   `json:"accepted"`
	DeliveryID string `json:"delivery_id"`
}

type Delivery struct {
	Raw json.RawMessage
}

type DeliveryPage struct {
	Items []Delivery
	Total int
}

type DeliveryDetail struct {
	Delivery Delivery
	Attempts []json.RawMessage
}

type DeliveryQuery struct {
	Limit           *int
	Offset          *int
	TriggerKind     *TriggerKind
	SourceOwnerType *string
	SourceOwnerID   *string
	State           *string
}
