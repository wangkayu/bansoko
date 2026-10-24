package httpapi

import (
	"encoding/json"
	"fmt"

	plugintargetapplication "bansoko/internal/plugintarget/application"
	"bansoko/internal/transport/httpapi/generated"
)

func mapPluginTargetInput(input generated.PluginTargetCreate) (plugintargetapplication.TargetInput, error) {
	config, err := json.Marshal(input.Config)
	if err != nil {
		return plugintargetapplication.TargetInput{}, fmt.Errorf("encode plugin target config: %w", err)
	}
	templates, err := json.Marshal(input.TemplateSet)
	if err != nil {
		return plugintargetapplication.TargetInput{}, fmt.Errorf("encode plugin target template set: %w", err)
	}
	secret, err := json.Marshal(input.Secret)
	if err != nil {
		return plugintargetapplication.TargetInput{}, fmt.Errorf("encode plugin target secret: %w", err)
	}
	triggers := make([]plugintargetapplication.TriggerKind, len(input.TriggerKinds))
	for i, trigger := range input.TriggerKinds {
		triggers[i] = plugintargetapplication.TriggerKind(trigger)
	}
	var envelope struct {
		Mode  string          `json:"mode"`
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(secret, &envelope); err != nil {
		return plugintargetapplication.TargetInput{}, fmt.Errorf("decode plugin target secret: %w", err)
	}
	return plugintargetapplication.TargetInput{
		AdapterKind: plugintargetapplication.AdapterKind(input.AdapterKind), Config: config,
		ConfigSchemaVersion: input.ConfigSchemaVersion, Enabled: input.Enabled, Name: input.Name,
		PluginKind: plugintargetapplication.PluginKind(input.PluginKind), Secret: plugintargetapplication.SecretInput{Mode: envelope.Mode, Value: envelope.Value},
		TemplateSet: templates, TriggerKinds: triggers,
	}, nil
}

func mapPluginTargetUpdate(input generated.PluginTargetUpdate) (plugintargetapplication.TargetUpdateInput, error) {
	create, err := mapPluginTargetInput(generated.PluginTargetCreate{
		AdapterKind: input.AdapterKind, Config: input.Config, ConfigSchemaVersion: input.ConfigSchemaVersion,
		Enabled: input.Enabled, Name: input.Name, PluginKind: input.PluginKind, Secret: generated.PluginTargetCreateSecret{},
		TemplateSet: input.TemplateSet, TriggerKinds: input.TriggerKinds,
	})
	if err != nil {
		return plugintargetapplication.TargetUpdateInput{}, err
	}
	secret, err := json.Marshal(input.Secret)
	if err != nil {
		return plugintargetapplication.TargetUpdateInput{}, fmt.Errorf("encode plugin target update secret: %w", err)
	}
	var envelope struct {
		Mode  string          `json:"mode"`
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(secret, &envelope); err != nil {
		return plugintargetapplication.TargetUpdateInput{}, fmt.Errorf("decode plugin target update secret: %w", err)
	}
	create.Secret = plugintargetapplication.SecretInput{Mode: envelope.Mode, Value: envelope.Value}
	return plugintargetapplication.TargetUpdateInput{TargetInput: create, ExpectedVersion: int64(input.ExpectedVersion)}, nil
}

func mapPluginTargetTestInput(input generated.PluginTargetTestInput) plugintargetapplication.TargetTestInput {
	return plugintargetapplication.TargetTestInput{ExpectedVersion: int64(input.ExpectedVersion), TriggerKind: plugintargetapplication.TriggerKind(input.TriggerKind)}
}

func mapPluginTargetConfig(target plugintargetapplication.TargetConfig) (generated.PluginTargetConfig, error) {
	// The application read already withheld anything that is not a canonical
	// allowlisted document; a null config is served as null with its status.
	var config *generated.PluginAdapterConfig
	if len(target.Config) > 0 && string(target.Config) != "null" {
		decoded := generated.PluginAdapterConfig{}
		if err := json.Unmarshal(target.Config, &decoded); err != nil {
			return generated.PluginTargetConfig{}, fmt.Errorf("decode plugin target config: %w", err)
		}
		config = &decoded
	}
	var templates generated.NotificationTemplateSet
	if err := json.Unmarshal(target.TemplateSet, &templates); err != nil {
		return generated.PluginTargetConfig{}, fmt.Errorf("decode plugin target template set: %w", err)
	}
	triggers := make([]generated.NotificationTriggerKind, len(target.TriggerKinds))
	for i, trigger := range target.TriggerKinds {
		triggers[i] = generated.NotificationTriggerKind(trigger)
	}
	return generated.PluginTargetConfig{
		AdapterKind: generated.PluginAdapterKind(target.AdapterKind), Config: config,
		ConfigStatus:        generated.PluginAdapterConfigStatus(target.ConfigStatus),
		ConfigSchemaVersion: target.ConfigSchemaVersion, Enabled: target.Enabled, HasSecret: target.HasSecret, HasSigningSecret: target.HasSigningSecret,
		Id: generated.ID(target.ID), Name: target.Name, PluginKind: generated.PluginKind(target.PluginKind), TemplateSet: templates,
		TriggerKinds: triggers, UpdatedAt: target.UpdatedAt, Version: generated.Version(target.Version),
	}, nil
}

func mapPluginTargetPage(page plugintargetapplication.TargetPage) (generated.PluginTargetPage, error) {
	result := generated.PluginTargetPage{Targets: make([]generated.PluginTargetConfig, len(page.Targets))}
	for i, target := range page.Targets {
		mapped, err := mapPluginTargetConfig(target)
		if err != nil {
			return generated.PluginTargetPage{}, err
		}
		result.Targets[i] = mapped
	}
	return result, nil
}

func mapPluginTestAccepted(result plugintargetapplication.TestAccepted) generated.PluginTestAccepted {
	return generated.PluginTestAccepted{Accepted: generated.PluginTestAcceptedAccepted(result.Accepted), DeliveryId: generated.ID(result.DeliveryID)}
}

func mapPluginDelivery(raw plugintargetapplication.Delivery) (generated.PluginDelivery, error) {
	var delivery generated.PluginDelivery
	if err := json.Unmarshal(raw.Raw, &delivery); err != nil {
		return generated.PluginDelivery{}, fmt.Errorf("decode plugin delivery: %w", err)
	}
	return delivery, nil
}

func mapPluginDeliveryPage(page plugintargetapplication.DeliveryPage) (generated.PluginDeliveryList, error) {
	result := generated.PluginDeliveryList{Items: make([]generated.PluginDelivery, len(page.Items)), Total: page.Total}
	for i, item := range page.Items {
		mapped, err := mapPluginDelivery(item)
		if err != nil {
			return generated.PluginDeliveryList{}, err
		}
		result.Items[i] = mapped
	}
	return result, nil
}

func mapPluginDeliveryDetail(detail plugintargetapplication.DeliveryDetail) (generated.PluginDeliveryDetail, error) {
	result := generated.PluginDeliveryDetail{Attempts: make([]generated.PluginDeliveryAttempt, len(detail.Attempts))}
	var err error
	result.Delivery, err = mapPluginDelivery(detail.Delivery)
	if err != nil {
		return generated.PluginDeliveryDetail{}, err
	}
	for i, raw := range detail.Attempts {
		if err := json.Unmarshal(raw, &result.Attempts[i]); err != nil {
			return generated.PluginDeliveryDetail{}, fmt.Errorf("decode plugin delivery attempt: %w", err)
		}
	}
	return result, nil
}

func mapPluginDeliveryQuery(params generated.ListPluginDeliveriesParams) plugintargetapplication.DeliveryQuery {
	query := plugintargetapplication.DeliveryQuery{Limit: params.Limit, Offset: params.Offset}
	if params.TriggerKind != nil {
		value := plugintargetapplication.TriggerKind(*params.TriggerKind)
		query.TriggerKind = &value
	}
	if params.SourceOwnerType != nil {
		value := string(*params.SourceOwnerType)
		query.SourceOwnerType = &value
	}
	if params.SourceOwnerId != nil {
		value := string(*params.SourceOwnerId)
		query.SourceOwnerID = &value
	}
	if params.State != nil {
		value := string(*params.State)
		query.State = &value
	}
	return query
}
