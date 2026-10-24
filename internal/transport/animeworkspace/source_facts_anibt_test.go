package animeworkspace

import (
	"testing"

	readmodelapplication "bansoko/internal/anime/readmodel/application"
)

func TestMapWorkspaceSourceFactsAniBT(t *testing.T) {
	if mapped := mapWorkspaceSourceFacts(readmodelapplication.SourceFacts{IdentityKind: "guid", IdentityValue: "g"}); mapped.Anibt != nil {
		t.Fatal("non-AniBT source facts must not carry anibt")
	}
	mapped := mapWorkspaceSourceFacts(readmodelapplication.SourceFacts{IdentityKind: "guid", IdentityValue: "g", AniBT: &readmodelapplication.SourceAniBTTags{Schema: "anibt-rss-1.0", Tags: []string{"language:CHS"}}})
	if mapped.Anibt == nil || mapped.Anibt.Schema == nil || *mapped.Anibt.Schema != "anibt-rss-1.0" || len(mapped.Anibt.Tags) != 1 || mapped.Anibt.Diagnostic != nil {
		t.Fatalf("anibt = %+v", mapped.Anibt)
	}
}

func TestMapWorkspaceSourceFactsTitleTags(t *testing.T) {
	if mapped := mapWorkspaceSourceFacts(readmodelapplication.SourceFacts{IdentityKind: "guid", IdentityValue: "g"}); mapped.TitleTags != nil {
		t.Fatal("a release without frozen title tags must not carry title_tags")
	}
	mapped := mapWorkspaceSourceFacts(readmodelapplication.SourceFacts{IdentityKind: "guid", IdentityValue: "g", TitleTags: &readmodelapplication.SourceTitleTags{Schema: "reltitle-attrs-v1", Implementation: "reltitle@ca671af", ConfigDigest: "lexicon:sha256:x", Tags: []string{"resolution:1080p"}}})
	if mapped.TitleTags == nil || mapped.TitleTags.Implementation != "reltitle@ca671af" || mapped.TitleTags.ConfigDigest == nil || len(mapped.TitleTags.Tags) != 1 || mapped.TitleTags.Diagnostic != nil {
		t.Fatalf("title_tags = %+v", mapped.TitleTags)
	}
}
