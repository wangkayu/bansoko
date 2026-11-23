package domain

import "bansoko/internal/qualityprofile"

func init() {
	register("NativeComparisonV1", Custom(func(r reader, value Value) error {
		m, err := r.closed(value, "profile", "candidate", "existing")
		if err != nil {
			return err
		}
		profile, err := qualityprofile.Parse(Canonical(m["profile"]))
		if err != nil {
			return r.at("profile").fail(CodeConstraintViolated)
		}
		readSubject := func(key string) (qualityprofile.Subject, error) {
			raw := m[key]
			if raw.Kind != KindObject {
				return qualityprofile.Subject{}, r.at(key).fail(CodeTypeInvalid)
			}
			want := []string{"values", "score"}
			if _, hasSize := raw.Object["size_bytes"]; hasSize {
				want = append(want, "size_bytes")
			}
			fields, err := r.at(key).closed(raw, want...)
			if err != nil {
				return qualityprofile.Subject{}, err
			}
			values, err := qualityprofile.ParseValues(Canonical(fields["values"]))
			if err != nil {
				return qualityprofile.Subject{}, r.at(key).at("values").fail(CodeConstraintViolated)
			}
			subject := qualityprofile.Subject{Values: values.Values}
			if fields["score"].Kind != KindNull {
				if err := I64.validate(r.at(key).at("score"), fields["score"]); err != nil {
					return subject, err
				}
				if values.Subgroup == nil || values.Subgroup.Provider == qualityprofile.CustomSubgroupProvider {
					return subject, r.at(key).at("score").fail(CodeConstraintViolated)
				}
				score := fields["score"].Int
				subject.Score = &score
			}
			if sizeVal, ok := fields["size_bytes"]; ok && sizeVal.Kind != KindNull {
				if err := I64.validate(r.at(key).at("size_bytes"), sizeVal); err != nil {
					return subject, err
				}
				if sizeVal.Int < 0 {
					return subject, r.at(key).at("size_bytes").fail(CodeConstraintViolated)
				}
				size := sizeVal.Int
				subject.SizeBytes = &size
			}
			return subject, nil
		}
		candidate, err := readSubject("candidate")
		if err != nil {
			return err
		}
		// A scan-managed incumbent with no release evidence is replaceable by
		// a candidate with evidence; no fabricated old subject is constructed.
		if m["existing"].Kind == KindNull {
			return nil
		}
		existing, err := readSubject("existing")
		if err != nil {
			return err
		}
		if !qualityprofile.Compare(candidate, existing, profile).Better {
			return r.fail(CodeConstraintViolated)
		}
		return nil
	}))
}
