package templatefacts

// Notification payloads are stored under the registry fact paths: the key a
// producer writes is the path a template reads, and there is no translation
// layer between the two.

// NotificationPayloadKeys is the key universe of a stored notification
// payload: every registered notification fact, keyed by its own registry
// path (the identity set — payload keys are registry paths). vars.<group>.<name>
// payload keys are dynamic (the frozen Vars output) and are not listed; they
// pass every payload-key filter by shape, not by membership.
var NotificationPayloadKeys = func() map[string]string {
	out := map[string]string{}
	for _, fact := range All() {
		if fact.Scenarios&scenarioNotify == 0 {
			continue
		}
		out[fact.Path] = fact.Path
	}
	return out
}()
