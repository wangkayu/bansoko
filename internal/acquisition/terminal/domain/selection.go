package domain

import "sort"

// SelectionMode is how an acquisition chose which files of a torrent to take.
type SelectionMode string

const (
	// SelectionRules selects by the frozen file rules, identified by their digest.
	SelectionRules SelectionMode = "rules"
	// SelectionExplicitRefs selects exactly the listed files of the verified manifest.
	SelectionExplicitRefs SelectionMode = "explicit_refs"
)

// Selection is SelectionV1. By rules it has a known rules digest and no refs; by
// explicit refs it has a non-empty, sorted, duplicate-free ref list and no rules
// digest. Refs name manifest files and only mean something against that manifest.
type Selection struct {
	Mode        SelectionMode
	RulesDigest *DigestText
	ExplicitRef []ID
}

// DecodeSelection decodes SelectionV1 from a parsed value.
func DecodeSelection(v Value) (Selection, error) { return reader{}.selection(v) }

func (r reader) selection(v Value) (Selection, error) {
	m, err := r.closed(v, "mode", "rules_digest", "explicit_refs")
	if err != nil {
		return Selection{}, err
	}
	if m["mode"].Kind != KindString {
		return Selection{}, r.at("mode").fail(CodeTypeInvalid)
	}
	mode := SelectionMode(m["mode"].String)
	if mode != SelectionRules && mode != SelectionExplicitRefs {
		return Selection{}, r.at("mode").fail(CodeUnionBranchInvalid)
	}
	out := Selection{Mode: mode}
	if m["rules_digest"].Kind != KindNull {
		d, err := r.at("rules_digest").digest(m["rules_digest"])
		if err != nil {
			return Selection{}, err
		}
		out.RulesDigest = &d
	}
	if m["explicit_refs"].Kind != KindArray {
		return Selection{}, r.at("explicit_refs").fail(CodeTypeInvalid)
	}
	for i, item := range m["explicit_refs"].Array {
		ref, err := r.at("explicit_refs").at(itoa(i)).id(item)
		if err != nil {
			return Selection{}, err
		}
		out.ExplicitRef = append(out.ExplicitRef, ref)
	}
	switch mode {
	case SelectionRules:
		if out.RulesDigest == nil {
			return Selection{}, r.at("rules_digest").fail(CodeNullNotAllowed)
		}
		if len(out.ExplicitRef) != 0 {
			return Selection{}, r.at("explicit_refs").fail(CodeConstraintViolated)
		}
	case SelectionExplicitRefs:
		if out.RulesDigest != nil {
			return Selection{}, r.at("rules_digest").fail(CodeConstraintViolated)
		}
		if len(out.ExplicitRef) == 0 {
			return Selection{}, r.at("explicit_refs").fail(CodeConstraintViolated)
		}
		if !sort.SliceIsSorted(out.ExplicitRef, func(i, j int) bool { return out.ExplicitRef[i] < out.ExplicitRef[j] }) {
			return Selection{}, r.at("explicit_refs").fail(CodeConstraintViolated)
		}
		for i := 1; i < len(out.ExplicitRef); i++ {
			if out.ExplicitRef[i] == out.ExplicitRef[i-1] {
				return Selection{}, r.at("explicit_refs").fail(CodeConstraintViolated)
			}
		}
	}
	return out, nil
}

// Value is the wire form of the selection.
func (s Selection) Value() Value {
	digest := null()
	if s.RulesDigest != nil {
		digest = str(string(*s.RulesDigest))
	}
	refs := make([]Value, 0, len(s.ExplicitRef))
	for _, ref := range s.ExplicitRef {
		refs = append(refs, str(string(ref)))
	}
	return obj(map[string]Value{"mode": str(string(s.Mode)), "rules_digest": digest, "explicit_refs": arr(refs)})
}
