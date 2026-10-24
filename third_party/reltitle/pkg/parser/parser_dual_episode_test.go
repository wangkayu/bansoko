package parser

import (
	"testing"

	"github.com/wangkayu/reltitle/pkg/config"
	"github.com/wangkayu/reltitle/pkg/model"
)

func TestParserDualEpisode_SeasonEpisodeWithParenAbsolute(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "【豌豆字幕组】[关于我转生变成史莱姆这档事 第四季 / Tensei Shitara Slime Datta Ken S4][01(73)][简体][1080P][MP4]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if got := res.ContentKind; got != "TV_EP" {
		t.Fatalf("content_kind=%q want=%q", got, "TV_EP")
	}
	if got := canonicalSeason(res); got != "4" {
		t.Fatalf("season=%q want=%q", got, "4")
	}
	if got := canonicalEpisode(res); got != "1" {
		t.Fatalf("episode=%q want=%q", got, "1")
	}
	alts := canonicalEPAlternates(res)
	if len(alts) != 1 {
		t.Fatalf("alternates=%v want len=1", alts)
	}
	if got := alts[0].Axis; got != "ABS" {
		t.Fatalf("alt.axis=%q want=%q", got, "ABS")
	}
	if got := alts[0].Episode; got != "73" {
		t.Fatalf("alt.episode=%q want=%q", got, "73")
	}
	if got := alts[0].Relation; got != "overall" {
		t.Fatalf("alt.relation=%q want=%q", got, "overall")
	}
	if aliases := canonicalAliases(res); len(aliases) != 1 || aliases[0] != "Tensei Shitara Slime Datta Ken" {
		t.Fatalf("aliases=%v want=[Tensei Shitara Slime Datta Ken]", aliases)
	}
}

func TestParserDualEpisode_SxxExxWithParenAbsolute(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Group] Title S01E01(13) [1080p]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if got := canonicalSeason(res); got != "1" {
		t.Fatalf("season=%q want=%q", got, "1")
	}
	if got := canonicalEpisode(res); got != "1" {
		t.Fatalf("episode=%q want=%q", got, "1")
	}
	alts := canonicalEPAlternates(res)
	if len(alts) != 1 || alts[0].Episode != "13" || alts[0].Axis != "ABS" {
		t.Fatalf("alternates=%v want ABS episode 13", alts)
	}
}

func TestParserDualEpisode_DashSeasonEpisodeWithParenAbsolute(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Group] Title 3 - 12(36) [1080p]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if got := canonicalSeason(res); got != "3" {
		t.Fatalf("season=%q want=%q", got, "3")
	}
	if got := canonicalEpisode(res); got != "12" {
		t.Fatalf("episode=%q want=%q", got, "12")
	}
	alts := canonicalEPAlternates(res)
	if len(alts) != 1 || alts[0].Episode != "36" || alts[0].Axis != "ABS" {
		t.Fatalf("alternates=%v want ABS episode 36", alts)
	}
}

func TestParserDualEpisode_RangeWithParenAbsoluteRange(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Group] Title [TV 01-10(17-26) Fin+SP][1080p]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if got := canonicalEPKind(res); got != "RANGE" {
		t.Fatalf("ep_semantics.kind=%q want=%q", got, "RANGE")
	}
	if got := canonicalEpisodeRange(res); got != "1-10" {
		t.Fatalf("episode_range=%q want=%q", got, "1-10")
	}
	alts := canonicalEPAlternates(res)
	if len(alts) != 1 {
		t.Fatalf("alternates=%v want len=1", alts)
	}
	if got := alts[0].Kind; got != "RANGE" {
		t.Fatalf("alt.kind=%q want=%q", got, "RANGE")
	}
	if got := alts[0].RangeStart + "-" + alts[0].RangeEnd; got != "17-26" {
		t.Fatalf("alt.range=%q want=%q", got, "17-26")
	}
}

func TestParserDualEpisode_SummaryEpisodeWithCatalogSxxExxAlternate(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[三明治摆烂组&Pre-S] 与游戏中心的少女异文化交流的故事 / Game Center Shoujo to Ibunka Kouryuu - 06.5 总集篇(S00E01) - [简繁日内封][H265 1080P](检索：游乐场少女的异文化交流)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if got := res.ContentKind; got != "TV_EP" {
		t.Fatalf("content_kind=%q want=%q", got, "TV_EP")
	}
	if got := canonicalEpisode(res); got != "6.5" {
		t.Fatalf("episode=%q want=%q", got, "6.5")
	}
	if got := canonicalSeason(res); got != "" {
		t.Fatalf("season=%q want empty", got)
	}
	alts := canonicalEPAlternates(res)
	if len(alts) != 1 {
		t.Fatalf("alternates=%v want len=1", alts)
	}
	if got := alts[0].Axis; got != "SE" {
		t.Fatalf("alt.axis=%q want=%q", got, "SE")
	}
	if got := alts[0].Season; got != "0" {
		t.Fatalf("alt.season=%q want=%q", got, "0")
	}
	if got := alts[0].Episode; got != "1" {
		t.Fatalf("alt.episode=%q want=%q", got, "1")
	}
	if got := alts[0].Relation; got != "catalog" {
		t.Fatalf("alt.relation=%q want=%q", got, "catalog")
	}
	if aliases := canonicalAliases(res); len(aliases) != 2 || aliases[0] != "游乐场少女的异文化交流" || aliases[1] != "Game Center Shoujo to Ibunka Kouryuu" {
		t.Fatalf("aliases=%v want cleaned aliases", aliases)
	}
}

func TestParserDualEpisode_SeasonEchoLeadWithParenOverall(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)
	res, err := p.Parse("【豌豆字幕组】[药屋少女的呢喃（药师少女的独语）/ Kusuriya no Hitorigoto S3][03(51)][简体][1080P][MP4]")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != model.ContentKindTVEP {
		t.Fatalf("content_kind=%q want=TV_EP", got)
	}
	if got := canonicalSeason(res); got != "3" {
		t.Fatalf("season=%q want=3", got)
	}
	if got := canonicalEpisode(res); got != "3" {
		t.Fatalf("episode=%q want=3", got)
	}
	alts := canonicalEPAlternates(res)
	if len(alts) != 1 || alts[0].Episode != "51" || alts[0].Relation != "overall" {
		t.Fatalf("alternates=%+v want one overall 51", alts)
	}
}

func TestParserDualEpisode_UnderscoreOverallInFileName(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)
	res, err := p.Parse("[BeanSub][Kusuriya no Hitorigoto S3][03_51][CHS][1080P][x264_AAC].mp4")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != model.ContentKindTVEP {
		t.Fatalf("content_kind=%q want=TV_EP", got)
	}
	if got := canonicalSeason(res); got != "3" {
		t.Fatalf("season=%q want=3", got)
	}
	if got := canonicalEpisode(res); got != "3" {
		t.Fatalf("episode=%q want=3", got)
	}
	alts := canonicalEPAlternates(res)
	if len(alts) != 1 || alts[0].Episode != "51" || alts[0].Relation != "overall" {
		t.Fatalf("alternates=%+v want one overall 51", alts)
	}
	if got := canonicalTitle(res); got != "Kusuriya no Hitorigoto" {
		t.Fatalf("title=%q want=Kusuriya no Hitorigoto", got)
	}
}

func TestParserDualEpisode_UnderscoreNeedsSeasonEvidence(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)
	for _, raw := range []string{
		"[BeanSub][Kusuriya no Hitorigoto][04_52][CHS][1080P].mp4",
		"[Group][Title S1][01_12][1080P].mp4",
		"[Group][Title S3][12_01][1080P].mp4",
	} {
		res, err := p.Parse(raw)
		if err != nil {
			t.Fatalf("%s: parse failed: %v", raw, err)
		}
		if alts := canonicalEPAlternates(res); len(alts) != 0 {
			t.Fatalf("%s: alternates=%+v want none", raw, alts)
		}
		if got := canonicalEpisode(res); got != "" {
			t.Fatalf("%s: episode=%q want none", raw, got)
		}
	}
}
