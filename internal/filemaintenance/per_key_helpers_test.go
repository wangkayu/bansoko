package filemaintenance

import (
	"fmt"

	acquisitiontarget "bansoko/internal/acquisition/target"
	"bansoko/internal/transport/httpapi/generated"
)

// evaluateSingle unwraps an evaluation of a single-key release: exactly one
// decision is frozen.
func evaluateSingle(views []generated.ReplacementDecisionView, err error) (generated.ReplacementDecisionView, error) {
	if err != nil {
		return generated.ReplacementDecisionView{}, err
	}
	if len(views) != 1 {
		return generated.ReplacementDecisionView{}, fmt.Errorf("evaluate froze %d decisions, want 1", len(views))
	}
	return views[0], nil
}

// releaseIntent folds per-key intents: replace only when every key replaces.
func releaseIntent(intents map[string]acquisitiontarget.Intent, err error) (acquisitiontarget.Intent, error) {
	if err != nil || len(intents) == 0 {
		return acquisitiontarget.IntentEnsurePresent, err
	}
	for _, intent := range intents {
		if intent != acquisitiontarget.IntentReplace {
			return acquisitiontarget.IntentEnsurePresent, nil
		}
	}
	return acquisitiontarget.IntentReplace, nil
}
