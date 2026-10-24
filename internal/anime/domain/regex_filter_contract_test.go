package domain

import "testing"

func TestRegexSectionRejectsLegacyMissingNullAndInvalid(t *testing.T) {
	for _, section := range []string{SectionReleaseRules, SectionFileRules} {
		for _, input := range []string{`{}`, `{"must_contain":""}`, `{"must_contain":null,"not_in":""}`, `{"must_contain":"[","not_in":""}`, `{"must_contain":"","not_in":"","include_keywords":[]}`, `{"include_extensions":[],"exclude_keywords":[]}`} {
			if err := ValidateSectionValue(section, []byte(input)); err == nil {
				t.Errorf("%s accepted %s", section, input)
			}
		}
		if err := ValidateSectionValue(section, []byte(`{"must_contain":"(?i)1080p|720p","not_in":"sample"}`)); err != nil {
			t.Fatal(err)
		}
	}
	for _, input := range []string{`{"languages":[]}`, `{"keep_all":true}`} {
		if err := ValidateSectionValue(SectionSubtitleRules, []byte(input)); err == nil {
			t.Errorf("accepted retired subtitle input %s", input)
		}
	}
}
