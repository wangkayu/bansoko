package domain

// PlacementMode is how a one-shot or subscribed acquisition relates to a managed
// instance's placement.
type PlacementMode string

const (
	// PlacementNone gives the acquisition no placement relationship at all.
	PlacementNone PlacementMode = "none"
	// PlacementObserveForInstance hands the delivered files to the named instance's
	// placement afterwards. It is an intent about a later management step; the
	// acquisition itself never depends on the instance staying alive.
	PlacementObserveForInstance PlacementMode = "observe_for_instance"
)

// Placement is PlacementV1: only the mode for none; the instance reference and the
// expected placement for observe_for_instance. A none placement that carries
// instance keys is refused, never ignored.
type Placement struct {
	Mode              PlacementMode
	ManagedAnimeRef   ID
	ExpectedPlacement FV[RefVersion]
}

var placementBranches = map[string][]string{
	string(PlacementNone):               {},
	string(PlacementObserveForInstance): {"managed_anime_ref", "expected_placement"},
}

// DecodePlacement decodes PlacementV1 from a parsed value.
func DecodePlacement(v Value) (Placement, error) { return reader{}.placement(v) }

func (r reader) placement(v Value) (Placement, error) {
	mode, m, err := r.branch(v, "mode", placementBranches)
	if err != nil {
		return Placement{}, err
	}
	if PlacementMode(mode) == PlacementNone {
		return Placement{Mode: PlacementNone}, nil
	}
	ref, err := r.at("managed_anime_ref").id(m["managed_anime_ref"])
	if err != nil {
		return Placement{}, err
	}
	expected, err := decodeFV(r.at("expected_placement"), m["expected_placement"], reader.refVersion)
	if err != nil {
		return Placement{}, err
	}
	return Placement{Mode: PlacementObserveForInstance, ManagedAnimeRef: ref, ExpectedPlacement: expected}, nil
}

// Value is the wire form of the placement.
func (p Placement) Value() Value {
	if p.Mode == PlacementNone {
		return obj(map[string]Value{"mode": str(string(PlacementNone))})
	}
	return obj(map[string]Value{
		"mode":               str(string(PlacementObserveForInstance)),
		"managed_anime_ref":  str(string(p.ManagedAnimeRef)),
		"expected_placement": p.ExpectedPlacement.value(RefVersion.value),
	})
}
