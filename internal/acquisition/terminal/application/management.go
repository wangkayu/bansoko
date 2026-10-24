package application

func managementQualificationValue(m qualificationMaterial) any {
	if m.managementNone {
		return map[string]any{"kind": "none"}
	}
	mg := m.management
	policy := "automatic_active"
	if m.entryMode == "subscription_direct" {
		policy = "manual_active_or_stopped"
	}
	var parserVersion any
	if m.config.kind == "owner" {
		parserVersion = m.parserVersion
	}
	var rulesVersion any
	if m.config.kind == "owner" {
		rulesVersion = m.rulesOwnerVersion
	}
	var subgroupsVersion any
	if m.config.kind == "owner" {
		subgroupsVersion = m.subgroupsOwnerVersion
	}
	return map[string]any{"kind": "instance", "managed_anime_ref": mg.managedID, "subscription_ref": mg.subscriptionID, "subscription_policy": policy, "expected_subscription_version": mg.subscriptionVersion, "expected_lifecycle_version": mg.lifecycleVersion, "expected_metadata_version": metadataVersionOf(m), "expected_parser_policy_version": parserVersion, "expected_rules_owner_version": rulesVersion, "expected_subgroups_owner_version": subgroupsVersion}
}
