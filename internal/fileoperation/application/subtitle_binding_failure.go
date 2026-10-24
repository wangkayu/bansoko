package application

// subtitleBindingFailure must run before final-set membership checks: missing
// and ambiguous have no bound ref by definition, and retain their typed reason.
func subtitleBindingFailure(state string) error {
	switch state {
	case bindingStateBound:
		return nil
	case bindingStateAmbiguous:
		return ErrSubtitleBindingAmbiguous
	default:
		return ErrSubtitleBindingMissing
	}
}
