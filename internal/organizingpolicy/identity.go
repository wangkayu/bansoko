package organizingpolicy

// Season distinguishes an explicit zero from missing. No truthiness/default
// season logic is permitted; identity/parser ownership remains outside here.
type Season struct {
	Value   uint32
	Present bool
}

type UnclassifiedReason string

const (
	Unknown              UnclassifiedReason = "unknown"
	SpecialWithoutNumber UnclassifiedReason = "special_without_number"
	RangeOrList          UnclassifiedReason = "range_or_list"
)

type UnclassifiedRoute string

const (
	Skip  UnclassifiedRoute = "skip"
	Route UnclassifiedRoute = "route"
)

type UnclassifiedDecision struct {
	Applicable bool
	Reason     string
	// Unclassified routing never creates coverage or replacement eligibility.
	ReplacementEligible bool
}

// DecideUnclassified does not render or validate templates. Route callers must
// use validated explicit unknown templates, never an implicit SP/Season00 path.
// Parser configuration errors are supplied separately and must remain failures.
func DecideUnclassified(eligible bool, reason UnclassifiedReason, route UnclassifiedRoute, parserFailed bool) UnclassifiedDecision {
	if !eligible {
		return UnclassifiedDecision{Reason: "filtered"}
	}
	if parserFailed {
		return UnclassifiedDecision{Reason: "parser_error"}
	}
	switch reason {
	case Unknown, SpecialWithoutNumber, RangeOrList:
	default:
		return UnclassifiedDecision{Reason: "invalid_unclassified_reason"}
	}
	switch route {
	case Skip:
		return UnclassifiedDecision{Reason: string(reason) + "_skip"}
	case Route:
		return UnclassifiedDecision{Applicable: true, Reason: string(reason) + "_route"}
	default:
		return UnclassifiedDecision{Reason: "invalid_unclassified_route"}
	}
}
