package model

import "encoding/json"

type parseResultWire struct {
	RawTitle           string       `json:"raw_title"`
	ContentKind        string       `json:"content_kind,omitempty"`
	ContentConfidence  float64      `json:"content_confidence,omitempty"`
	TemplateID         string       `json:"template_id,omitempty"`
	TemplateConfidence float64      `json:"template_confidence,omitempty"`
	EPSemantics        *EPSemantics `json:"ep_semantics,omitempty"`
	AudioSlot          *AudioSlot   `json:"audio_slot,omitempty"`
	Attributes         []Attribute  `json:"attributes,omitempty"`
	Tokens             []Token      `json:"tokens,omitempty"`
}

func (r ParseResult) MarshalJSON() ([]byte, error) {
	wire := parseResultWire{
		RawTitle:           r.RawTitle,
		ContentKind:        string(r.ContentKind),
		ContentConfidence:  r.ContentConfidence,
		TemplateID:         r.TemplateID,
		TemplateConfidence: r.TemplateConfidence,
		EPSemantics:        r.CanonicalEPSemantics(),
		AudioSlot:          r.AudioSlot,
		Attributes:         filterSerializedAttributes(r.Attributes),
		Tokens:             r.Tokens,
	}
	return json.Marshal(wire)
}

func filterSerializedAttributes(attrs []Attribute) []Attribute {
	if len(attrs) == 0 {
		return nil
	}
	out := make([]Attribute, 0, len(attrs))
	for _, a := range attrs {
		switch a.Type {
		case AttrEpisode, AttrEpisodeList, AttrEpisodeRange, AttrSeason, AttrSeasonRange, AttrSPKind:
			continue
		default:
			out = append(out, a)
		}
	}
	return out
}
