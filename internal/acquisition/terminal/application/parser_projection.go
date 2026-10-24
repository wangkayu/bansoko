package application

import "bansoko/internal/parser"

func nativeParserProvenance(p parser.Provenance, selection any) any {
	optional := func(value any, present bool) any {
		if present {
			return frozenValue("known", value)
		}
		return frozenValue("absent", nil)
	}
	return map[string]any{
		"selection":      selection,
		"source_version": optional(p.SourceVersion, p.SourceVersion > 0), "policy_version": optional(p.PolicyVersion, p.PolicyVersion > 0), "profile_version": optional(p.ProfileVersion, p.ProfileVersion > 0), "rules_owner_version": optional(p.RulesOwnerVersion, p.RulesOwnerVersion > 0),
		"input_digest": optional(p.InputDigest, p.InputDigest != ""), "result_digest": optional(p.ResultDigest, p.ResultDigest != ""), "media_kind": optional(p.MediaKind, p.MediaKind != ""), "season": frozenValue("known", p.Season), "season_bound": frozenValue("known", p.SeasonBound), "parse_status": optional(p.ParseStatus, p.ParseStatus != ""), "parse_shape": optional(p.ParseShape, p.ParseShape != ""),
	}
}
