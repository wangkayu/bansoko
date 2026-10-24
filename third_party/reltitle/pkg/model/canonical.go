package model

import "strings"

func (r *ParseResult) attrValues(t AttrType) []string {
	if r == nil || len(r.Attributes) == 0 {
		return nil
	}
	out := make([]string, 0, 4)
	for _, a := range r.Attributes {
		if a.Type != t {
			continue
		}
		v := strings.TrimSpace(a.Normalized)
		if v == "" {
			continue
		}
		out = append(out, v)
	}
	return out
}

func (r *ParseResult) firstAttrValue(t AttrType) string {
	for _, v := range r.attrValues(t) {
		return v
	}
	return ""
}

func (r *ParseResult) CanonicalTitle() string {
	return r.firstAttrValue(AttrTitle)
}

func (r *ParseResult) CanonicalGroup() string {
	return r.firstAttrValue(AttrGroup)
}

func (r *ParseResult) CanonicalTitleAliases() []string {
	return r.attrValues(AttrTitleAlias)
}

func (r *ParseResult) CanonicalSearchHints() []string {
	return r.attrValues(AttrSearchHint)
}

func cloneEPReference(in *EPReference) *EPReference {
	if in == nil {
		return nil
	}
	cp := *in
	if len(in.EpisodeList) > 0 {
		cp.EpisodeList = append([]string(nil), in.EpisodeList...)
	}
	return &cp
}

func cloneEPSemantics(in *EPSemantics) *EPSemantics {
	if in == nil {
		return nil
	}
	cp := *in
	cp.Primary = cloneEPReference(in.Primary)
	if len(in.Alternates) > 0 {
		cp.Alternates = make([]EPReference, 0, len(in.Alternates))
		for _, alt := range in.Alternates {
			altCopy := alt
			if len(alt.EpisodeList) > 0 {
				altCopy.EpisodeList = append([]string(nil), alt.EpisodeList...)
			}
			cp.Alternates = append(cp.Alternates, altCopy)
		}
	}
	return &cp
}

func (r *ParseResult) CanonicalEPSemantics() *EPSemantics {
	if r == nil {
		return nil
	}
	return cloneEPSemantics(r.EPSemantics)
}

func (r *ParseResult) CanonicalEpisode() string {
	if sem := r.CanonicalEPSemantics(); sem != nil && sem.Primary != nil {
		return strings.TrimSpace(sem.Primary.Episode)
	}
	return ""
}

func (r *ParseResult) CanonicalEpisodeRange() string {
	if sem := r.CanonicalEPSemantics(); sem != nil && sem.Primary != nil {
		start := strings.TrimSpace(sem.Primary.RangeStart)
		end := strings.TrimSpace(sem.Primary.RangeEnd)
		if start != "" && end != "" {
			return start + "-" + end
		}
	}
	return ""
}

func (r *ParseResult) CanonicalSeason() string {
	if sem := r.CanonicalEPSemantics(); sem != nil && sem.Primary != nil {
		return strings.TrimSpace(sem.Primary.Season)
	}
	return ""
}

func (r *ParseResult) CanonicalSeasonRange() string {
	if sem := r.CanonicalEPSemantics(); sem != nil && sem.Primary != nil {
		return strings.TrimSpace(sem.Primary.SeasonRange)
	}
	return ""
}

func (r *ParseResult) CanonicalVersion() string {
	return r.firstAttrValue(AttrVersion)
}

func (r *ParseResult) CanonicalPackKind() string {
	return r.firstAttrValue(AttrPackKind)
}

func (r *ParseResult) CanonicalPackProfile() string {
	return r.firstAttrValue(AttrPackProfile)
}

func (r *ParseResult) CanonicalSPKind() string {
	if sem := r.CanonicalEPSemantics(); sem != nil && sem.Primary != nil {
		return strings.TrimSpace(sem.Primary.SPKind)
	}
	return ""
}

func (r *ParseResult) CanonicalMovieIndex() string {
	return r.firstAttrValue(AttrMovieIndex)
}

func (r *ParseResult) CanonicalMovieIndexRange() string {
	return r.firstAttrValue(AttrMovieIndexRange)
}

func (r *ParseResult) CanonicalEpRoute() string {
	if r == nil {
		return ""
	}
	sem := r.CanonicalEPSemantics()
	if sem == nil || sem.Primary == nil {
		return ""
	}
	k := sem.Primary.Kind.Canonical()
	source := sem.Primary.Source.Canonical()
	season := strings.TrimSpace(sem.Primary.Season)
	switch k {
	case EPKindRange:
		return "RANGE"
	case EPKindEPList:
		return "EP_LIST"
	case EPKindSP:
		return "SP_ONLY"
	case EPKindEP:
		if source == EPSourceTaggedEPFinal {
			return "ABS_EP"
		}
		if season != "" {
			return "SXXEXX"
		}
		return "PLAIN_EP"
	case EPKindSeason, EPKindSeasonRange:
		return "NONE"
	default:
		return ""
	}
}
