package frozenpolicy

import (
	domain "bansoko/internal/anime/domain"
	"bansoko/internal/attrrules"
	"bansoko/internal/namingvars"
	"bansoko/internal/qualityprofile"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// SeedBusiness persists real source programs and returns an independent complete
// submitted business value. No fixture guard bypass or active Vars is used.
type ProgramWriter interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func SeedBusiness(ctx context.Context, db ProgramWriter, source string) (domain.BusinessSnapshot, error) {
	b := domain.BusinessSnapshot{SchemaVersion: domain.BusinessSnapshotSchemaVersion, Sections: map[string]json.RawMessage{}, SectionVersions: map[string]int64{}, SectionSchemas: map[string]string{}, Parser: domain.ParserPolicyEnvelope{SchemaVersion: domain.ParserPolicyEnvelopeSchemaVersion, Kind: "regex", SourceOwner: "global", Provenance: "global", PolicyVersion: 1, ProfileVersion: 1, RulesOwnerVersion: 1}, RetentionVersion: 1, FileEffectVersion: 1, UnclassifiedRoute: "skip"}
	for _, name := range domain.EffectiveSectionOrder {
		value, err := domain.DefaultSectionValue(name)
		if err != nil {
			return b, err
		}
		b.Sections[name], err = json.Marshal(value)
		if err != nil {
			return b, err
		}
		b.SectionVersions[name] = 1
		b.SectionSchemas[name] = domain.SectionSchemaVersions[name]
	}
	b.Retention = domain.RetentionValue{AdmissionMode: "all", QualityProfile: qualityprofile.DefaultJSON(), CleanupPolicy: json.RawMessage(`{"version":"cleanup-v1","effect":"quarantine","grace_seconds":60}`)}
	varsDigest, err := namingvars.SourceFingerprint(source)
	if err != nil {
		return b, err
	}
	raw, _ := json.Marshal(source)
	if _, err = db.ExecContext(ctx, `INSERT INTO naming_vars_versions(fingerprint,content,active) VALUES($1,$2,FALSE) ON CONFLICT(fingerprint) DO NOTHING`, varsDigest, raw); err != nil {
		return b, err
	}
	rules := attrrules.Rules{Version: 1, Resolution: []string{"1080p"}, Quality: []string{"web"}, Language: []string{"jpn"}, SpecialMarkers: []string{"special"}, GroupBrackets: []string{"[", "]"}}
	attrDigest, err := attrrules.Fingerprint(rules)
	if err != nil {
		return b, err
	}
	raw, _ = json.Marshal(rules)
	if _, err = db.ExecContext(ctx, `INSERT INTO attr_rule_versions(fingerprint,content,active) VALUES($1,$2,FALSE) ON CONFLICT(fingerprint) DO NOTHING`, attrDigest, raw); err != nil {
		return b, fmt.Errorf("seed exact attr program: %w", err)
	}
	b.Programs = domain.ProgramReferences{Vars: namingvars.ProgramRef{Digest: varsDigest, Version: namingvars.ProfileVersion(varsDigest)}, AttrRules: namingvars.ProgramRef{Digest: attrDigest, Version: attrrules.ProfileVersion(attrDigest)}}
	return b.Freeze()
}
