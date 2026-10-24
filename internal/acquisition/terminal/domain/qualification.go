package domain

import "strconv"

var positiveU64Text = Custom(func(r reader, v Value) error {
	if v.Kind != KindString || v.String == "" || v.String[0] == '0' {
		return r.fail(CodeTypeInvalid)
	}
	n, err := strconv.ParseUint(v.String, 10, 64)
	if err != nil || n == 0 || strconv.FormatUint(n, 10) != v.String {
		return r.fail(CodeTypeInvalid)
	}
	return nil
})

// QualificationV1 is short-lived backend capture evidence. It is not persisted,
// transport authorization, or an open-ended map of objects a caller may lock.
func init() {
	register("GroupScopeV1", Obj(F("provider", Enum("anibt", "mikan")), F("catalog_entry_ref", positiveU64Text)))
	register("ConfigQualificationV1", Union("kind", map[string]*objectSchema{
		"submitted":      Obj(F("request_hash", DigestOf), F("config_digest", DigestOf)),
		"saved_revision": Obj(F("candidate_ref", IDType), F("candidate_version", Int(1, maxI64)), F("revision", Int(1, maxI64)), F("revision_digest", DigestOf)),
		"owner":          Obj(F("managed_anime_ref", IDType), F("config_version", Int(1, maxI64)), F("config_digest", DigestOf)),
	}))
	register("ManagementQualificationV1", Union("kind", map[string]*objectSchema{
		"none":     Obj(),
		"instance": Obj(F("managed_anime_ref", IDType), F("subscription_ref", IDType), F("subscription_policy", Enum("automatic_active", "manual_active_or_stopped")), F("expected_subscription_version", Int(1, maxI64)), F("expected_lifecycle_version", Int(1, maxI64)), F("expected_metadata_version", Nullable(Int(1, maxI64))), F("expected_parser_policy_version", Nullable(Int(1, maxI64))), F("expected_rules_owner_version", Nullable(Int(1, maxI64))), F("expected_subgroups_owner_version", Nullable(Int(1, maxI64)))),
	}))
	register("SourceQualificationV1", Union("kind", map[string]*objectSchema{
		"release": Obj(F("release_ref", IDType), F("expected_version", Int(1, maxI64)), F("source_ref", Ref("RefVersion"))),
		"seed":    Obj(F("seed_identity_digest", DigestOf)),
	}))
	register("ObservationQualificationV1", Obj(F("scope", Ref("GroupScopeV1")), F("snapshot_ref", IDType), F("snapshot_version", Int(1, maxI64)), F("observation_digest", DigestOf)))
	register("FeedQualificationV1", Obj(F("snapshot_ref", IDType), F("snapshot_version", Int(1, maxI64)), F("scope", Ref("GroupScopeV1")), F("group_ref", Text(1, 128)),
		F("content_hash", DigestOf), F("hash_schema", Enum("provider-feed-snapshot-v1")), F("projection_schema", Enum("torrent-manifest-v1")), F("stable_seed_key", Text(1, 256)), F("payload_digest", DigestOf), F("manifest_digest", DigestOf)))
	register("TranslationIdentityV1", Obj(F("schema_version", Text(1, 128)), F("data_version", Text(1, 128)), F("content_digest", DigestOf)))
	register("ProgramQualificationV1", Obj(F("attr_fingerprint", DigestOf), F("vars_digest", Nullable(DigestOf)), F("translation", Nullable(Ref("TranslationIdentityV1")))))
	register("PlacementQualificationV1", Union("kind", map[string]*objectSchema{
		"none": Obj(), "existing": Obj(F("placement_ref", IDType), F("expected_revision", Int(1, maxI64)), F("file_fact_digest", DigestOf)),
	}))
	shape := Obj(F("config", Ref("ConfigQualificationV1")), F("management", Ref("ManagementQualificationV1")), F("source", Ref("SourceQualificationV1")),
		F("observation", Nullable(Ref("ObservationQualificationV1"))), F("feed", Nullable(Ref("FeedQualificationV1"))), F("programs", Ref("ProgramQualificationV1")), F("placement", Ref("PlacementQualificationV1")))
	register("QualificationV1", Custom(func(r reader, v Value) error {
		if err := shape.validate(r, v); err != nil {
			return err
		}
		mg := v.Object["management"]
		if mg.Object["kind"].String == "instance" {
			if (v.Object["config"].Object["kind"].String == "owner") != (mg.Object["expected_subgroups_owner_version"].Kind == KindInteger) {
				return r.at("management").at("expected_subgroups_owner_version").fail(CodeConstraintViolated)
			}
			version := mg.Object["expected_metadata_version"]
			if (v.Object["config"].Object["kind"].String == "owner") != (version.Kind == KindInteger) {
				return r.at("management").at("expected_metadata_version").fail(CodeConstraintViolated)
			}
			if (v.Object["config"].Object["kind"].String == "owner") != (mg.Object["expected_rules_owner_version"].Kind == KindInteger) {
				return r.at("management").at("expected_rules_owner_version").fail(CodeConstraintViolated)
			}
			if (v.Object["config"].Object["kind"].String == "owner") != (mg.Object["expected_parser_policy_version"].Kind == KindInteger) {
				return r.at("management").at("expected_parser_policy_version").fail(CodeConstraintViolated)
			}
		}
		return nil
	}))
}

// Qualification keeps the validated seven-member value private. Its constructor
// never loads live state; the accepting transaction must recheck every claim.
type Qualification struct{ value Value }

func DecodeQualification(raw []byte) (Qualification, error) {
	v, err := ParseStrict(raw)
	if err != nil {
		return Qualification{}, err
	}
	if err := Validate("QualificationV1", v); err != nil {
		return Qualification{}, err
	}
	return Qualification{value: v}, nil
}
func (q Qualification) Canonical() []byte { return Canonical(q.value) }
