package application

// retainedAcceptanceEnabled keeps historical assertion bodies type-checked
// while their explicit replacement owns execution. This is not configurable.
func retainedAcceptanceEnabled() bool { return false }
