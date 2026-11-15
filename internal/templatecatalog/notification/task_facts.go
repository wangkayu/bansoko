package notification

// TaskOrigin projects the acquisition entry kind, never managed anime metadata.
// The SQL twin is notification_task_origin (migration 137). No acquisition or
// an unrecognized entry supplies no origin; neither is guessed as subscription.
func TaskOrigin(entryKind string) any {
	switch entryKind {
	case "automatic", "subscription_add":
		return "subscription"
	case "one_shot":
		return "one_shot"
	default:
		return nil
	}
}
