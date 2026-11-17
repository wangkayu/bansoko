package frozenpolicy

import (
	domain "bansoko/internal/anime/domain"
	"bansoko/internal/namingvars"
	"bansoko/internal/qualityprofile"
	"context"
	"database/sql"
	"encoding/json"
)

// SeedBusiness persists real source programs and returns an independent complete
// submitted business value. No fixture guard bypass or active Vars is used.
type ProgramWriter interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func SeedBusiness(ctx context.Context, db ProgramWriter, source string) (domain.BusinessSnapshot, error) {
	b := domain.BusinessSnapshot{SchemaVersion: domain.BusinessSnapshotSchemaVersion, Sections: map[string]json.RawMessage{}, SectionVersions: map[string]int64{}, SectionSchemas: map[string]string{}, Parser: domain.ParserPolicyEnvelope{SchemaVersion: domain.ParserPolicyEnvelopeSchemaVersion, Kind: "regex", SourceOwner: "global", Provenance: "global", PolicyVersion: 1, ProfileVersion: 1, RulesOwnerVersion: 1}, ReplacementVersion: 1, FileEffectVersion: 1, UnclassifiedRoute: "skip"}
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
	b.Replacement = domain.ReplacementValue{AdmissionMode: "all", QualityProfile: qualityprofile.DefaultJSON()}
	varsDigest, err := namingvars.SourceFingerprint(source)
	if err != nil {
		return b, err
	}
	raw, _ := json.Marshal(source)
	if _, err = db.ExecContext(ctx, `INSERT INTO naming_vars_versions(fingerprint,content,active) VALUES($1,$2,FALSE) ON CONFLICT(fingerprint) DO NOTHING`, varsDigest, raw); err != nil {
		return b, err
	}
	b.Programs = domain.ProgramReferences{Vars: namingvars.ProgramRef{Digest: varsDigest, Version: namingvars.ProfileVersion(varsDigest)}}
	return b.Freeze()
}
