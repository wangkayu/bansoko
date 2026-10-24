package parser

import (
	"strings"
	"testing"

	"github.com/wangkayu/reltitle/pkg/config"
	"github.com/wangkayu/reltitle/pkg/model"
)

func TestParserAliasCleanup_StripsTVAnimePrefixAndTVSeasonTail(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[BDMV] 赛马娘/TVアニメ「ウマ娘 プリティーダービー Season 3」Vol.1 (Umamusume Season3)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	title := canonicalTitle(res)

	if strings.Contains(strings.ToLower(title), "tv アニメ") {
		t.Fatalf("title still contains tv anime prefix: %q", title)
	}
	if strings.Contains(strings.ToLower(title), "vol") {
		t.Fatalf("title still contains vol suffix: %q", title)
	}

	for _, a := range canonicalAliases(res) {
		la := strings.ToLower(a)
		if strings.Contains(la, "tv アニメ") || strings.Contains(la, "tv anime") {
			t.Fatalf("alias still contains tv anime prefix: %q", a)
		}
		if strings.Contains(la, "tv - 2") || strings.Contains(la, "тв - 2") {
			t.Fatalf("alias still contains tv season tail: %q", a)
		}
	}
}

func TestParserAliasCleanup_StripsTechTailFromTitle(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Niconeiko Works] Colorful Stage! The Movie A Miku Who Can`t Sing Ma10p FLAC DTS-HDMA"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	title := canonicalTitle(res)

	lc := strings.ToLower(title)
	if strings.Contains(lc, "ma10p") || strings.Contains(lc, "flac") || strings.Contains(lc, "dts") || strings.Contains(lc, "hdma") {
		t.Fatalf("title still contains tech tail: %q", title)
	}
}

func TestParserAliasCleanup_DropsPreviewVersionAlias(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[天月搬運組] 不死不運 / UNDEAD UNLUCK - 01 [NetFilx][WEB-RIP][1080p][AVC AAC][CHT][SRT][MKV](先行版本)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	for _, a := range canonicalAliases(res) {
		if strings.Contains(a, "先行版") || strings.Contains(a, "先行版本") {
			t.Fatalf("preview tag leaked into alias list: %q", a)
		}
	}
}

func TestParserAliasCleanup_SlashTitleNotMerged(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[VCB-Studio] 柔光魔女股份有限公司 / Kabushikigaisha Magi-Lumière / 株式会社マジルミエ 10-bit 1080p HEVC BDRip [Fin]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	title := canonicalTitle(res)
	aliases := canonicalAliases(res)

	if strings.Contains(title, "/") {
		t.Fatalf("slash title should not be merged: %q", title)
	}
	if title != "柔光魔女股份有限公司" {
		t.Fatalf("unexpected title: %q", title)
	}
	if !containsAlias(aliases, "株式会社マジルミエ") || !containsAlias(aliases, "Kabushikigaisha Magi Lumière") {
		t.Fatalf("unexpected aliases: %v", aliases)
	}
}

func TestParserAliasCleanup_DelicosSlashTitleNotMerged(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[ANi] Delicos Nursery / TRUMP 系列 Delico’s Nursery - 08 [1080P][Baha][WEB-DL][AAC AVC][CHT][MP4]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	title := canonicalTitle(res)
	aliases := canonicalAliases(res)

	if strings.Contains(title, "/") {
		t.Fatalf("slash title should not be merged: %q", title)
	}
	if title != "Delicos Nursery" {
		t.Fatalf("unexpected primary title: %q", title)
	}
	if !containsAlias(aliases, "TRUMP 系列 Delico’s Nursery") {
		t.Fatalf("expected series-bridge alias, got: %v", aliases)
	}
}

func TestParserAliasCleanup_DelicosNoSlashKeepsFullTitle(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[ANi] TRUMP 系列 Delico’s Nursery - 08 [1080P][Baha][WEB-DL][AAC AVC][CHT][MP4]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	title := canonicalTitle(res)
	aliases := canonicalAliases(res)

	if title == "系列" {
		t.Fatalf("mixed-script fallback over-split title, got=%q aliases=%v", title, aliases)
	}
	if !strings.Contains(title, "TRUMP") || !strings.Contains(title, "Nursery") {
		t.Fatalf("unexpected title after cleanup: %q aliases=%v", title, aliases)
	}
	for _, bad := range []string{"TRUMP", "Delico’s", "Nursery"} {
		if containsAlias(aliases, bad) {
			t.Fatalf("single-token alias leak (%q): %v", bad, aliases)
		}
	}
}

func TestParserAliasCleanup_SingleSlashSeasonBridgeAddsAlias(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[ANi] Kingdom S06 / 王者天下 第六季 - 01 [1080P][Baha][WEB-DL][AAC AVC][CHT][MP4]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	title := canonicalTitle(res)
	aliases := canonicalAliases(res)

	if title != "Kingdom" {
		t.Fatalf("unexpected title: %q aliases=%v", title, aliases)
	}
	if !containsAlias(aliases, "王者天下") {
		t.Fatalf("expected chinese alias extracted from single-slash season bridge, got=%v", aliases)
	}
}

func TestParserAliasCleanup_SingleSlashNoSeasonSignalNotForced(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[TestGroup] Fate/Zero - 01 [1080p][WEB-DL]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	aliases := canonicalAliases(res)
	if containsAlias(aliases, "Fate") {
		t.Fatalf("single-slash fallback should not add left-side alias, aliases=%v", aliases)
	}
}

func TestParserAliasCleanup_SingleSlashSpecialBridgeAddsAlias(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[GJ.Y] 我內心的糟糕念頭 [特別篇] / Boku no Kokoro no Yabai Yatsu - 01 (Baha 1920x1080 AVC AAC MP4)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	aliases := canonicalAliases(res)
	if !containsAlias(aliases, "Boku no Kokoro no Yabai Yatsu") {
		t.Fatalf("expected special-bridge slash alias extracted, got=%v", aliases)
	}
}

func TestParserAliasCleanup_StripsSPKindTailFromAlias(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "【幻櫻字幕組】【黃金神威 Golden Kamuy】【OVA】【05】【BDrip】【BIG5_MP4】【1280X720】"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	aliases := canonicalAliases(res)
	if containsAlias(aliases, "Golden Kamuy OVA") {
		t.Fatalf("sp-kind tail leaked into alias list: %v", aliases)
	}
	if !containsAlias(aliases, "Golden Kamuy") {
		t.Fatalf("expected cleaned alias, got=%v", aliases)
	}
}

func TestParserEpisodeDetect_TitleSeasonSuffixDoesNotPolluteEPSlot(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[桜都字幕组] 魔都精兵的奴隶 第二季 / Mato Seihei no Slave 2 [09][1080P][简体内嵌]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalEpisode(res); got != "9" {
		t.Fatalf("episode=%q want=%q", got, "9")
	}
	if got := canonicalSeason(res); got != "2" {
		t.Fatalf("season=%q want=%q", got, "2")
	}
	if got := canonicalTitle(res); got != "魔都精兵的奴隶" {
		t.Fatalf("title=%q want=%q", got, "魔都精兵的奴隶")
	}
}

func TestParserEpisodeDetect_SeasonWordDashEpisodeCleansTitle(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[shincaps] Saikyou no Ousama, Nidome no Jinsei wa Nani wo Suru Season 2 - 01 (AT-X 1440x1080 MPEG2 AAC).ts"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalSeason(res); got != "2" {
		t.Fatalf("season=%q want=%q", got, "2")
	}
	if got := canonicalEpisode(res); got != "1" {
		t.Fatalf("episode=%q want=%q", got, "1")
	}
	if got := canonicalEpisodeRange(res); got != "" {
		t.Fatalf("episode_range=%q want empty", got)
	}
	if got := canonicalTitle(res); got != "Saikyou no Ousama, Nidome no Jinsei wa Nani wo Suru" {
		t.Fatalf("title=%q want=%q", got, "Saikyou no Ousama, Nidome no Jinsei wa Nani wo Suru")
	}
}

func TestParserEpisodeDetect_DashSeasonSuffixBeforeBracketPrefersBracketEpisode(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[黑白字幕组]0岁儿童开始冲刺的故事 第二季/0-saiji Start Dash Monogatari -2  [01] [AVC-8bit 1080P AAC][CHS]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalEpisode(res); got != "1" {
		t.Fatalf("episode=%q want=%q", got, "1")
	}
	if got := canonicalSeason(res); got != "2" {
		t.Fatalf("season=%q want=%q", got, "2")
	}
}

func TestParserEpisodeDetect_PartIndexDoesNotBecomeSeason_BilingualTitle(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[黒ネズミたち] Dr.STONE 新石纪 第四季 / Dr. Stone: Science Future Part 3 - 25 (CR 1920x1080 AVC AAC MKV)"
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
	if got := canonicalEpisode(res); got != "25" {
		t.Fatalf("episode=%q want=%q", got, "25")
	}
	if got := canonicalEpisodeRange(res); got != "" {
		t.Fatalf("episode_range=%q want empty", got)
	}
	if got := canonicalTitle(res); got != "Dr.STONE 新石纪" {
		t.Fatalf("title=%q want=%q", got, "Dr.STONE 新石纪")
	}
	aliases := canonicalAliases(res)
	if !containsAlias(aliases, "Dr.Stone Science Future Part 3") {
		t.Fatalf("expected english part alias to remain, aliases=%v", aliases)
	}
}

func TestParserEpisodeDetect_PartIndexDoesNotBecomeSeason_EnglishTitle(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Dynamis One] Dr. Stone: Science Future Part 3 - 25 (CR 1920x1080 AVC AAC MKV) [323BB689].mkv"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "TV_EP" {
		t.Fatalf("content_kind=%q want=%q", got, "TV_EP")
	}
	if got := canonicalSeason(res); got != "" {
		t.Fatalf("season=%q want empty", got)
	}
	if got := canonicalEpisode(res); got != "25" {
		t.Fatalf("episode=%q want=%q", got, "25")
	}
	if got := canonicalEpisodeRange(res); got != "" {
		t.Fatalf("episode_range=%q want empty", got)
	}
	if got := canonicalTitle(res); got != "Dr.Stone : Science Future Part 3" {
		t.Fatalf("title=%q want=%q", got, "Dr.Stone : Science Future Part 3")
	}
}

func TestParserAliasCleanup_DashBridgeBilingualSeasonAlias(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[ANi] AMAIM Warrior at the Borderline S2 - 境界戰機 第二部 - 15 [1080P][Baha][WEB-DL][AAC AVC][CHT][MP4]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	title := canonicalTitle(res)
	aliases := canonicalAliases(res)

	if title != "AMAIM Warrior at the Borderline" {
		t.Fatalf("unexpected title: %q aliases=%v", title, aliases)
	}
	if !containsAlias(aliases, "境界戰機") {
		t.Fatalf("expected bilingual dash alias extracted, got=%v", aliases)
	}
}

func TestParserAliasCleanup_DashBridge_NoSeasonSignalNotForced(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[TestGroup] To Be Hero X - Inherited Will - 12 [1080p][WEB-DL]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	aliases := canonicalAliases(res)
	if containsAlias(aliases, "Inherited Will") {
		t.Fatalf("dash bridge should not force alias without season/special signal, aliases=%v", aliases)
	}
}

func TestParserAliasCleanup_DashBridge_SeasonMarkerConsumedInTitleGap(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[ANi] AMAIM Warrior at the Borderline S2 - 境界戰機 第二部 - 15 [1080P][Baha][WEB-DL][AAC AVC][CHT][MP4]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	for _, tok := range res.Tokens {
		if strings.TrimSpace(tok.Text) == "第二部" && !tok.Removed {
			t.Fatalf("season marker in title-gap should be consumed, tokens=%v", res.Tokens)
		}
	}
}

func TestParserAliasCleanup_TitleGapBilingualRunAlias_NoDelimiter(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[云光字幕组]夏目友人帐S07 Natsume Yuujinchou S07[01][简体双语][1080p]招募翻译"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	aliases := canonicalAliases(res)
	if !containsAlias(aliases, "Natsume Yuujinchou") {
		t.Fatalf("expected no-delimiter bilingual alias extracted, got=%v", aliases)
	}
}

func TestParserAliasCleanup_PromoteInlineBracketContinuationIntoTitle(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[shincaps] Futsuu no Joshikousei ga [Locodol] Yattemita. - 01 (AT-X 1440x1080 MPEG2 AAC).ts"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	title := canonicalTitle(res)
	aliases := canonicalAliases(res)
	if !strings.Contains(strings.ToLower(title), "locodol") || !strings.Contains(strings.ToLower(title), "yattemita") {
		t.Fatalf("inline bracket continuation should be promoted into title, got title=%q aliases=%v", title, aliases)
	}
	if containsAlias(aliases, "Locodol") {
		t.Fatalf("inline bracket token should be merged into title, not kept as alias: %v", aliases)
	}
}

func TestParserAliasCleanup_InlineBracketMetaNotPromoted(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[FênixFansub] Nukitashi the Animation [BD][VOL-01][PT-BR]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	title := canonicalTitle(res)
	if strings.Contains(strings.ToLower(title), "vol") || strings.Contains(strings.ToLower(title), "bd") {
		t.Fatalf("meta bracket should not be promoted into title, got=%q", title)
	}
}

func TestParserAliasCleanup_TitleGapAliasTokenWithSeasonSuffixConsumed(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[ANi] The Devil Is a PartTimer S2 - 打工吧，魔王大人！第二季 - 01 [1080P][Baha][WEB-DL][AAC AVC][CHT][MP4]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if canonicalTitle(res) != "The Devil Is a PartTimer" {
		t.Fatalf("unexpected title=%q", canonicalTitle(res))
	}
	if !containsAlias(canonicalAliases(res), "打工吧，魔王大人！") {
		t.Fatalf("expected cleaned zh alias, got=%v", canonicalAliases(res))
	}
	for _, tok := range res.Tokens {
		if strings.TrimSpace(tok.Text) == "打工吧，魔王大人！第二季" && !tok.Removed {
			t.Fatalf("alias token with embedded season suffix should be consumed, tokens=%v", res.Tokens)
		}
	}
}

func TestParserGroupDetection_LongBracketWithSubtitleKeyword(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[GreatMission字幕小队※急募时轴] 全员逃走中 动画版 Tousouchuu Great Mission - 14 [1080p][AVC][CHT][MP4]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	groups := attrValues(res, model.AttrGroup)
	if len(groups) == 0 {
		t.Fatalf("expected group detected from long subtitle bracket, got none")
	}
	if !strings.Contains(groups[0], "字幕") {
		t.Fatalf("unexpected group=%v", groups)
	}
}

func TestParserAliasCleanup_BatchTVMarkerConsumedInTitleGap(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "Kekkon Yubiwa Monogatari II | Tales of Wedding Rings [TV-2] [2025] [01 of 12] [WEBRip] [1080p] [RUS + JAP]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "BATCH" {
		t.Fatalf("content_kind=%q want=BATCH", got)
	}
	if got := canonicalSeason(res); got != "2" {
		t.Fatalf("season=%q want=%q", got, "2")
	}
	if got := canonicalTitle(res); got != "Kekkon Yubiwa Monogatari II" {
		t.Fatalf("title=%q want=%q", got, "Kekkon Yubiwa Monogatari II")
	}
	if got := canonicalAliases(res); !containsAlias(got, "Tales of Wedding Rings") {
		t.Fatalf("expected cleaned english alias, got=%v", got)
	}
	for _, tok := range res.Tokens {
		if (strings.EqualFold(strings.TrimSpace(tok.Text), "TV") || strings.EqualFold(strings.TrimSpace(tok.Text), "ТВ")) && !tok.Removed {
			t.Fatalf("batch title-gap TV marker should be consumed, tokens=%v", res.Tokens)
		}
	}
}

func TestParserGroupDetection_InlineStarTwoTokenPrefix(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "SFSub&六四位元字幕組★29歲單身中堅冒險家的日常 29-sai Dokushin Chuuken Boukensha no Nichijou★01★1920x1080★AVC AAC MP4★繁體中文"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	groups := attrValues(res, model.AttrGroup)
	if len(groups) == 0 {
		t.Fatalf("expected inline-star two-token group detected")
	}
	if !strings.Contains(groups[0], "字幕") {
		t.Fatalf("unexpected group=%v", groups)
	}
}

func TestParserAliasCleanup_SingleSlashMiniAnimeBridgeAddsAlias(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[黒ネズミたち] 小手指同學，請別亂摸 小劇場 / Sawaranaide Kotesashi-kun Mini Anime Gekijou - 01 (ABEMA 1920x1080 AVC AAC MP4)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	aliases := canonicalAliases(res)
	if !containsAlias(aliases, "Sawaranaide Kotesashi-kun Mini Anime Gekijou") {
		t.Fatalf("expected mini-anime slash alias extracted, got=%v", aliases)
	}
	if containsAlias(aliases, "kun Mini Anime Gekijou") {
		t.Fatalf("hyphen-split alias fragment should not leak, aliases=%v", aliases)
	}
	for _, tok := range res.Tokens {
		if strings.TrimSpace(tok.Text) == "小劇場" && !tok.Removed {
			t.Fatalf("mini-theater bridge marker should be consumed, tokens=%v", res.Tokens)
		}
	}
}

func TestParserSpecialMarker_WithEpisode_PrefersEpisodePrimary(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[ANi] 关于我转生变成史莱姆这档事 第三季 [特别篇] - 01 [1080P][Baha][WEB-DL][AAC AVC][CHT][MP4]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "TV_EP" {
		t.Fatalf("content_kind=%q want=%q", got, "TV_EP")
	}
	if got := canonicalEpisode(res); got != "1" {
		t.Fatalf("episode=%q want=%q", got, "1")
	}
	if got := canonicalSPKind(res); got != "SP" {
		t.Fatalf("sp_kind=%q want=%q", got, "SP")
	}
}

func TestParserSpecialMarker_WithoutEpisode_PromotesSPKind(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[ANi] DDDD 恶魔的破坏 [特别篇] [1080P][Baha][WEB-DL][AAC AVC][CHT][MP4]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalEpisode(res); got != "" {
		t.Fatalf("episode=%q want empty", got)
	}
	if got := canonicalSPKind(res); got != "SP" {
		t.Fatalf("sp_kind=%q want=%q", got, "SP")
	}
	if got := res.ContentKind; got != "SPECIAL" {
		t.Fatalf("content_kind=%q want=%q", got, "SPECIAL")
	}
}

func TestParserSPKind_StandaloneOVAWithEpisodeSlot(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "【幻櫻字幕組】【黃金神威 Golden Kamuy】【OVA】【05】【BDrip】【BIG5_MP4】【1280X720】"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalEpisode(res); got != "5" {
		t.Fatalf("episode=%q want=%q", got, "5")
	}
	if got := canonicalSPKind(res); got != "OVA" {
		t.Fatalf("sp_kind=%q want=%q", got, "OVA")
	}
}

func TestParserSPKind_OvaDashEpisodeSlot(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[shincaps] Futsuu no Joshikousei ga [Locodol] Yattemita. OVA - 01 (AT-X 1440x1080 MPEG2 AAC).ts"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalEpisode(res); got != "1" {
		t.Fatalf("episode=%q want=%q", got, "1")
	}
	if got := canonicalSPKind(res); got != "" {
		t.Fatalf("sp_kind should be empty for title-style OVA - 01, got=%q", got)
	}
	if title := canonicalTitle(res); !strings.Contains(strings.ToUpper(title), "OVA") {
		t.Fatalf("title should keep OVA word, got=%q", title)
	}
}

func TestParserSPKind_OvaAsTitleWord_NoSPKind(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Test] Futsuu no OVA Project - 01 [1080p][WEB-DL]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalEpisode(res); got != "1" {
		t.Fatalf("episode=%q want=%q", got, "1")
	}
	if got := canonicalSPKind(res); got != "" {
		t.Fatalf("sp_kind should be empty for title-word OVA, got=%q", got)
	}
	if title := canonicalTitle(res); !strings.Contains(strings.ToUpper(title), "OVA") {
		t.Fatalf("title should keep OVA word, got=%q", title)
	}
}

func TestParserAudioFlag_JpnDubPhraseConsumed(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[神楽坂 まひろ] 最后的召唤师（日配版） / The Last Summoner (JPN Dub) - 10 (B-Global Donghua 1920x1080 HEVC AAC MKV)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalEpisode(res); got != "10" {
		t.Fatalf("episode=%q want=%q", got, "10")
	}
	if got := firstAttrValue(res, model.AttrAudioFlag); got == "" {
		t.Fatalf("audio_flag should not be empty")
	}
	dubConsumed := false
	for _, tk := range res.Tokens {
		if strings.EqualFold(strings.TrimSpace(tk.Text), "dub") {
			dubConsumed = tk.Removed
			break
		}
	}
	if !dubConsumed {
		t.Fatalf("expected Dub token consumed by audio_flag rule")
	}
}

func TestParserAudioFlag_JpnDubCnMarkerRemovedFromTitle(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[黒ネズミたち] 时光代理人 英都篇（日配版） - 08 (ABEMA 1280x720 AVC AAC MP4)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalTitle(res); got != "时光代理人 英都篇" {
		t.Fatalf("title=%q want=%q", got, "时光代理人 英都篇")
	}
	if !hasAttrValue(attrValues(res, model.AttrAudioLang), "JPN") {
		t.Fatalf("expected audio_lang=JPN, got=%v", attrValues(res, model.AttrAudioLang))
	}
	if !hasAttrValue(attrValues(res, model.AttrAudioFlag), "DUB") {
		t.Fatalf("expected audio_flag=DUB, got=%v", attrValues(res, model.AttrAudioFlag))
	}
}

func TestParserAudioFlag_ChineseDubMarkerRemovedFromTitle(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Doomdos] - 名侦探柯南（中配） - 第1254话 - [1080P].mp4"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalTitle(res); got != "名侦探柯南" {
		t.Fatalf("title=%q want=%q", got, "名侦探柯南")
	}
	if !hasAttrValue(attrValues(res, model.AttrAudioLang), "CMN") {
		t.Fatalf("expected audio_lang=CMN, got=%v", attrValues(res, model.AttrAudioLang))
	}
	if !hasAttrValue(attrValues(res, model.AttrAudioFlag), "DUB") {
		t.Fatalf("expected audio_flag=DUB, got=%v", attrValues(res, model.AttrAudioFlag))
	}
}

func TestParserAudioFlag_ChineseDubMarkerRemovedFromSeasonTitle(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Doomdos] - 超常技能开启奇幻世界美食之旅 第二季（中配版） - 第08话 - [1080P].mp4"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalTitle(res); got != "超常技能开启奇幻世界美食之旅" {
		t.Fatalf("title=%q want=%q", got, "超常技能开启奇幻世界美食之旅")
	}
	if got := canonicalSeason(res); got != "2" {
		t.Fatalf("season=%q want=%q", got, "2")
	}
	if !hasAttrValue(attrValues(res, model.AttrAudioLang), "CMN") {
		t.Fatalf("expected audio_lang=CMN, got=%v", attrValues(res, model.AttrAudioLang))
	}
	if !hasAttrValue(attrValues(res, model.AttrAudioFlag), "DUB") {
		t.Fatalf("expected audio_flag=DUB, got=%v", attrValues(res, model.AttrAudioFlag))
	}
}

func TestParserSubtitleSignals_EnglishSubDoesNotLeakToAudio(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[ToonsHub] High School Kimengumi 2026 S01E12 1080p AMZN WEB-DL DDP2.0 H.264 (English-Sub)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if hasAttrValue(attrValues(res, model.AttrAudioLang), "ENG") {
		t.Fatalf("English-Sub should not produce audio_lang=ENG, got=%v", attrValues(res, model.AttrAudioLang))
	}
	if !hasAttrValue(attrValues(res, model.AttrSubLang), "ENG") {
		t.Fatalf("expected sub_lang=ENG, got=%v", attrValues(res, model.AttrSubLang))
	}
	if !hasAttrValue(attrValues(res, model.AttrSubFlag), "soft") {
		t.Fatalf("expected sub_flag=soft for English-Sub, got=%v", attrValues(res, model.AttrSubFlag))
	}
}

func TestParserSubtitleSignals_MultiSubsAliasDoesNotLeakFakeEnglish(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[ToonsHub] A Misanthrope Teaches a Class for Demi-Humans S01E12 1080p CR WEB-DL AAC2.0 H.264 (Jingai Kyoushitsu no Ningengirai Kyoushi, Multi-Subs)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if hasAttrValue(attrValues(res, model.AttrSubLang), "ENG") {
		t.Fatalf("romanized alias should not leak sub_lang=ENG, got=%v", attrValues(res, model.AttrSubLang))
	}
	if !hasAttrValue(attrValues(res, model.AttrSubLang), "MULTI") {
		t.Fatalf("expected sub_lang=MULTI, got=%v", attrValues(res, model.AttrSubLang))
	}
	if !hasAttrValue(attrValues(res, model.AttrSubFlag), "soft") {
		t.Fatalf("expected sub_flag=soft for Multi-Subs, got=%v", attrValues(res, model.AttrSubFlag))
	}
}

func TestParserSubtitleSignals_MultiSubSingleTokenRecognized(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Erai-raws] Niwatori Fighter - 03 [1080p CR WEB-DL AVC AAC][MultiSub][55DB271A]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !hasAttrValue(attrValues(res, model.AttrSubLang), "MULTI") {
		t.Fatalf("expected sub_lang=MULTI, got=%v", attrValues(res, model.AttrSubLang))
	}
	if !hasAttrValue(attrValues(res, model.AttrSubFlag), "soft") {
		t.Fatalf("expected sub_flag=soft, got=%v", attrValues(res, model.AttrSubFlag))
	}
}

func TestParserSpecialMarkerCnToken_IsConsumedAndMappedToContents(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[ANi] 关于我转生变成史莱姆这档事 第三季 [特别篇] - 01 [1080P][Baha][WEB-DL][AAC AVC][CHT][MP4]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalEpisode(res); got != "1" {
		t.Fatalf("episode=%q want=%q", got, "1")
	}
	if !hasAttrValue(attrValues(res, model.AttrContents), "SPECIAL") {
		t.Fatalf("contents should contain SPECIAL, got=%v", attrValues(res, model.AttrContents))
	}
	found := false
	for _, tk := range res.Tokens {
		if strings.TrimSpace(tk.Text) == "特别篇" {
			found = true
			if !tk.Removed {
				t.Fatalf("特别篇 token should be removed")
			}
		}
	}
	if !found {
		t.Fatalf("特别篇 token not found in token stream")
	}
}

func TestParserAliasCleanup_ParenAliasKeepsInnerClosingParen(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[桜都字幕组] 我怎么可能成为你的恋人，不行不行！（※不是不可能！？） / Watashi ga Koibito ni Nareru Wakenai jan, Muri Muri！（Muri ja Nakatta！？） [01][1080p][简体内嵌]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	aliases := canonicalAliases(res)
	if len(aliases) == 0 {
		t.Fatalf("expected alias, got none")
	}
	found := false
	for _, a := range aliases {
		if strings.Contains(a, "Nakatta！？）") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("inner closing paren should be kept, aliases=%v", aliases)
	}
}

func TestParserAliasCleanup_SearchHintParenPromotedToSearchHint(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[雪飘工作室][キミとアイドルプリキュア♪/You and Idol Precure♪/与你同为 偶像光之美少女♪][1080p][38（下周停播）][简繁外挂](检索:Q娃)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	aliases := canonicalAliases(res)
	for _, a := range aliases {
		la := strings.ToLower(a)
		if strings.Contains(la, "检索") || strings.Contains(la, "q 娃") || strings.Contains(la, "q娃") {
			t.Fatalf("search hint should not stay in alias: aliases=%v", aliases)
		}
	}
	hints := attrValues(res, model.AttrSearchHint)
	if len(hints) == 0 {
		t.Fatalf("expected search_hint attr from paren hint, got none")
	}
}

func TestParserAliasCleanup_DropsSeasonOnlyAliasSuffix(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[ANi] To Your Eternity S03 /  給不滅的你 第三季 - 01 [1080P][Baha][WEB-DL][AAC AVC][CHT][MP4]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	aliases := canonicalAliases(res)
	for _, a := range aliases {
		if strings.EqualFold(strings.TrimSpace(a), "To Your Eternity S03") {
			t.Fatalf("season suffix should be stripped from alias: %v", aliases)
		}
	}
}

func TestParserAliasCleanup_RebalanceGenericPrimaryFromAlias(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[DBD-Raws][4K Remastered][大魔神剧场版三部曲合集/Daimajin The Moive Trilogy 01-03][正片+特典映像][1080P][BDRip][HEVC-10bit][FLAC][MKV](大魔神劇場版三部作)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	title := canonicalTitle(res)
	if title == "正片" {
		t.Fatalf("generic primary title should be rebalanced, got=%q aliases=%v", title, canonicalAliases(res))
	}
	if !strings.Contains(title, "大魔神") {
		t.Fatalf("unexpected rebalanced title=%q", title)
	}
}

func TestParserAliasCleanup_RebalanceGenericMovieTitleFromLatinFragments(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[NC-Raws] BanG Dream！FILM LIVE - 電影 (Baha 1920x1080 AVC AAC MP4)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	title := canonicalTitle(res)
	if title == "電影" {
		t.Fatalf("generic movie marker should not remain primary title, got=%q aliases=%v", title, canonicalAliases(res))
	}
	if !strings.Contains(strings.ToLower(title), "bang") || !strings.Contains(strings.ToLower(title), "live") {
		t.Fatalf("unexpected rebalanced title=%q", title)
	}
}

func TestParserAliasCleanup_RebalanceGenericTheatricalTitleFromAlias(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "【动漫国字幕组】★剧场版[再见了 橡果兄弟! -奇迹的暑假-][BDRip][AVC_AAC][1080P][简体][MP4][v2]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	title := canonicalTitle(res)
	if title == "剧场版" || title == "劇場版" {
		t.Fatalf("generic theatrical marker should not remain primary title, got=%q aliases=%v", title, canonicalAliases(res))
	}
	if !strings.Contains(title, "橡果兄弟") {
		t.Fatalf("unexpected rebalanced title=%q aliases=%v", title, canonicalAliases(res))
	}
	for _, a := range canonicalAliases(res) {
		if strings.EqualFold(strings.TrimSpace(a), "v2") || strings.EqualFold(strings.TrimSpace(a), "ver2") {
			t.Fatalf("version marker leaked into aliases: %v", canonicalAliases(res))
		}
	}
}

func TestParserAliasCleanup_DropsResolutionMetaAliases(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	cases := []string{
		"(WHIM) Deltora Quest (2007) [480p JDVD] [H265] [Opus 2.0]",
		"[AI-Raws] 羅小黒戦記 ぼくが選ぶ未来 / 罗小黑战记 / The Legend of Hei [4K Reproduction] [MKV]",
		"[Moozzi2] Girls Band Cry [ 4K Ver. ]",
		"[Techmod Raws] Get Ride AMDriver (J-DVD RAW)",
		"[芝士动物朋友] 盒中之海 [B-WebDl][1716p(4K)+6ch][AVC+EAC3][简英内嵌]",
		"【合集】[bilibili]处刑少女的生存之道／処刑少女の生きる道[第一季—全][WebRip_1080P_AVC-HiL5+HEVC_MaL5_AAC_MKV][SRT-简繁内挂]",
	}
	for _, raw := range cases {
		res, err := p.Parse(raw)
		if err != nil {
			t.Fatalf("parse failed for %q: %v", raw, err)
		}
		for _, a := range canonicalAliases(res) {
			la := strings.ToLower(strings.TrimSpace(a))
			if strings.Contains(la, "4k") ||
				strings.Contains(la, "480p") ||
				strings.Contains(la, "fhd") ||
				strings.Contains(la, "srt") ||
				strings.Contains(la, "webdl") ||
				strings.Contains(la, "webrip") ||
				strings.Contains(la, "j-dvd") ||
				strings.Contains(la, "dvd raw") {
				t.Fatalf("meta alias leaked for %q: %v", raw, canonicalAliases(res))
			}
		}
	}
}

func TestParserAliasCleanup_DropsBitDepthAndSubtitleMetaAliases(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[4K数字修复／2K版][SUK字幕组][哥斯拉][1984][中日双语字幕][HDTVrip][AVC][10bit][1080P][MKV]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	aliases := canonicalAliases(res)
	for _, a := range aliases {
		la := strings.ToLower(strings.TrimSpace(a))
		if la == "10bit" || strings.Contains(a, "字幕") || strings.Contains(a, "双语") || strings.Contains(a, "雙語") {
			t.Fatalf("meta alias leaked: aliases=%v", aliases)
		}
	}
}

func TestParserMetaMarkerCleanup_ConsumesVolInDiscContext(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[BDMV] CITY THE ANIMATION Vol.1-2"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	for _, tok := range res.Tokens {
		if strings.EqualFold(strings.TrimSpace(tok.Text), "Vol") && !tok.Removed {
			t.Fatalf("vol token should be consumed in disc context, tokens=%v", res.Tokens)
		}
	}
}

func TestParserMetaMarkerCleanup_KeepsVolumeInTitleContext(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[神楽坂 まひろ] RWBY Volume 9 - 02 (CR 1920x1080 AVC AAC MKV)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	foundAlive := false
	for _, tok := range res.Tokens {
		if strings.EqualFold(strings.TrimSpace(tok.Text), "Volume") && !tok.Removed {
			foundAlive = true
			break
		}
	}
	if !foundAlive {
		t.Fatalf("volume token should remain in title context, tokens=%v", res.Tokens)
	}
}

func TestParserMetaMarkerCleanup_ConsumesEpisodesRangePrefix(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[DVD Remux] Tetsuwan Atom Astro Boy (1963) Volume 1 R3 DVD Remux [English Sub] Episodes 1~5"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	for _, tok := range res.Tokens {
		if strings.EqualFold(strings.TrimSpace(tok.Text), "Episodes") && !tok.Removed {
			t.Fatalf("episodes token should be consumed when followed by numeric range, tokens=%v", res.Tokens)
		}
	}
}

func TestParserSpecial_NCOPRouteAndTitleCleanup(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "Happy World! NCOP DVD"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.ContentKind != "SPECIAL" {
		t.Fatalf("content_kind=%q want=SPECIAL", res.ContentKind)
	}
	if res.TemplateID != "TF_TV_SP_ONLY_04" {
		t.Fatalf("template_id=%q want=TF_TV_SP_ONLY_04", res.TemplateID)
	}
	if got := canonicalSPKind(res); got != "NCOP" {
		t.Fatalf("sp_kind=%q want=NCOP", got)
	}
	if got := canonicalTitle(res); strings.Contains(strings.ToUpper(got), "NCOP") {
		t.Fatalf("title should strip NCOP suffix, got=%q", got)
	}
	for _, tok := range res.Tokens {
		if strings.EqualFold(strings.TrimSpace(tok.Text), "DVD") && !tok.Removed {
			t.Fatalf("DVD token should be consumed in NCOP route, tokens=%v", res.Tokens)
		}
	}
}

func TestParserSpecial_NCOPNCEDNotAsTailGroup(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[sandoe41] Death March kara Hajimaru Isekai Kyousoukyoku Season One NCOP-NCED"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalGroup(res); got != "sandoe41" {
		t.Fatalf("group=%q want=sandoe41", got)
	}
	if got := canonicalSPKind(res); got != "NCOP" {
		t.Fatalf("sp_kind=%q want=NCOP", got)
	}
	if got := firstAttrValue(res, model.AttrTailGroup); got != "" {
		t.Fatalf("tail_group should be empty, got=%q", got)
	}
}

func TestParserSpecial_PureNCEDRouteAndTitleCleanup(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Group] Sample Title NCED [1080p][WEB-DL][AAC AVC]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalSPKind(res); got != "NCED" {
		t.Fatalf("sp_kind=%q want=NCED", got)
	}
	if got := canonicalTitle(res); strings.Contains(strings.ToUpper(got), "NCED") {
		t.Fatalf("title should strip NCED suffix, got=%q", got)
	}
	if got := firstAttrValue(res, model.AttrTailGroup); got != "" {
		t.Fatalf("tail_group should be empty, got=%q", got)
	}
}

func TestParserSpecial_NCOPNCEDKeepsNCEDCandidate(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[sandoe41] Death March kara Hajimaru Isekai Kyousoukyoku Season One NCOP-NCED"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalSPKind(res); got != "NCOP" {
		t.Fatalf("sp_kind=%q want=NCOP", got)
	}
	if got := canonicalEPKind(res); got != "SP" {
		t.Fatalf("ep_semantics.kind=%q want=SP", got)
	}
}

func TestParserTitle_TailGroupSuffixTrimmedFromTitle(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "100 Meters 2025 1080p NF WEB-DL DUAL DDP5.1 H 264-VARYG (Hyakuemu., Dual-Audio, Multi-Subs)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalGroup(res); got != "VARYG" {
		t.Fatalf("group=%q want=%q", got, "VARYG")
	}
	if got := firstAttrValue(res, model.AttrTailGroup); got != "VARYG" {
		t.Fatalf("tail_group=%q want=%q", got, "VARYG")
	}
	if got := firstAttrValue(res, model.AttrSourceType); got != "WEB-DL" {
		t.Fatalf("source_type=%q want=%q", got, "WEB-DL")
	}
	title := canonicalTitle(res)
	if strings.Contains(strings.ToUpper(title), "VARYG") {
		t.Fatalf("title should not contain tail group, got=%q", title)
	}
}

func TestParserTitle_SpacedTailGroupSuffixTrimmedFromTitle(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "100 METERS (2025) MULTi 1080p WEB AV1 E-AC-3 -Tsundere-Raws (NF) (VF, FRENCH, VOSTFR, Hyakuemu.)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalGroup(res); got != "Tsundere-Raws" {
		t.Fatalf("group=%q want=%q", got, "Tsundere-Raws")
	}
	title := canonicalTitle(res)
	upper := strings.ToUpper(title)
	if strings.Contains(upper, "TSUNDERE") || strings.Contains(upper, "RAWS") {
		t.Fatalf("title should not contain spaced tail group suffix, got=%q", title)
	}
	for _, tok := range res.Tokens {
		txt := strings.ToLower(strings.TrimSpace(tok.Text))
		if (txt == "tsundere" || txt == "raws") && !tok.Removed {
			t.Fatalf("tail group tokens should be consumed, tokens=%v", res.Tokens)
		}
	}
}

func TestParserSpecial_CMNotDetectedFromLengthUnit(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Lilith-Raws] 無論何時我們的戀情都是10厘米。 / Itsudatte Bokura no Koi wa 10 cm Datta [01-06][Baha][WEB-DL][1080p][AVC AAC][CHT][MP4]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalSPKind(res); got == "CM" {
		t.Fatalf("cm length unit must not be detected as sp_kind, raw=%q", raw)
	}
}

func TestParserSpecial_BareExTitleWordNotDetectedAsSPKind(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[AI-Raws] Star Ocean EX 1080p WEB-DL AAC AVC"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalSPKind(res); got != "" {
		t.Fatalf("bare EX title word must not be detected as sp_kind, got=%q", got)
	}
}

func TestParserSpecial_BareExInTitlePhraseNotDetectedAsSPKind(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "Betrothed to My Sister's Ex S01E07 1080p WEB-DL AAC"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalSPKind(res); got != "" {
		t.Fatalf("title phrase Ex must not be detected as sp_kind, got=%q", got)
	}
}

func TestParserSpecial_BracketPhraseExNotDetectedAsSPKind(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[DBD-Raws][little busters ex][1080P][BDRip][HEVC-10bit][FLAC][MKV]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalSPKind(res); got != "" {
		t.Fatalf("bracket phrase ex must not be detected as sp_kind, got=%q", got)
	}
}

func TestParserSpecial_EX01StillDetectedAsSPKind(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Group] Sample Title EX01 [1080p][WEB-DL]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalSPKind(res); got != "EX" {
		t.Fatalf("EX01 should keep sp_kind=EX, got=%q", got)
	}
}

func TestParserSpecial_DashEXWithoutStrongContextNotDetectedAsSPKind(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Group] Sample Title - EX [1080p][WEB-DL]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalSPKind(res); got != "" {
		t.Fatalf("dash EX without strong context must not be detected as sp_kind, got=%q", got)
	}
}

func TestParserSpecial_IsolatedBracketEXStillDetectedAsSPKind(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Group] Sample Title [EX] [1080p][WEB-DL]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalSPKind(res); got != "EX" {
		t.Fatalf("isolated bracket EX should keep sp_kind=EX, got=%q", got)
	}
}

func TestParserNotePromotion_EndToReleaseStatus(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Group][Sample Title][12FIN][1080p][WEB-DL]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !hasAttrValue(attrValues(res, model.AttrReleaseStatus), "FIN") {
		t.Fatalf("expected release_status=FIN, got=%v", attrValues(res, model.AttrReleaseStatus))
	}
	if hasAttrValue(attrValues(res, model.AttrNote), "FIN") {
		t.Fatalf("FIN should be promoted from note, notes=%v", attrValues(res, model.AttrNote))
	}
}

func TestDropRedundantNoteAttributes_RemovesMetaDuplicates(t *testing.T) {
	attrs := []model.Attribute{
		{Type: model.AttrPlatform, Normalized: "VIUTV"},
		{Type: model.AttrResolution, Normalized: "1080p"},
		{Type: model.AttrSourceType, Normalized: "WEB-DL"},
		{Type: model.AttrPackKind, Normalized: "tv_pack"},
		{Type: model.AttrContents, Normalized: "MOVIE"},
		{Type: model.AttrNote, Normalized: "VIUTV"},
		{Type: model.AttrNote, Normalized: "1080p"},
		{Type: model.AttrNote, Normalized: "WEB-DL"},
		{Type: model.AttrNote, Normalized: "Season 2"},
		{Type: model.AttrNote, Normalized: "S2"},
		{Type: model.AttrNote, Normalized: "Season 1-3"},
		{Type: model.AttrNote, Normalized: "S1-S3"},
		{Type: model.AttrNote, Normalized: "TV_PACK"},
		{Type: model.AttrNote, Normalized: "MOVIE"},
		{Type: model.AttrNote, Normalized: "RECRUITMENT"},
	}
	got := dropRedundantNoteAttributes(attrs, "2", "S1-S3")
	notes := make([]string, 0, 8)
	for _, a := range got {
		if a.Type == model.AttrNote {
			notes = append(notes, a.Normalized)
		}
	}
	if len(notes) != 1 || !strings.EqualFold(strings.TrimSpace(notes[0]), "RECRUITMENT") {
		t.Fatalf("expected only RECRUITMENT note to remain, notes=%v", notes)
	}
}

func TestParserSpecial_DashSpecialPhrasePromotesEPSlotPrimarySP(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[ANi] Kaijuu 8 gou S02 / 怪獸 8 號 第二季 - 特別篇 [1080P][Baha][WEB-DL][AAC AVC][CHT][MP4]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if sem := canonicalEPSemantics(res); sem == nil {
		t.Fatalf("ep_semantics should be promoted for dash special phrase")
	}
	if got := canonicalEPKind(res); got != "SP" {
		t.Fatalf("ep_semantics.kind=%q want=SP", got)
	}
	if got := canonicalSPKind(res); got != "SP" {
		t.Fatalf("sp_kind=%q want=SP", got)
	}
	if got := canonicalSPKind(res); got != "SP" {
		t.Fatalf("canonical sp_kind=%q want=SP", got)
	}
}

func TestParserSpecial_DashSPBeforeTechParenPromotesSpecial(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[shincaps] Marika-chan no Koukando wa Bukkowareteiru - SP (BS11 1920x1080 MPEG2 AAC).ts"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalTitle(res); got != "Marika-chan no Koukando wa Bukkowareteiru" {
		t.Fatalf("title=%q want=%q", got, "Marika-chan no Koukando wa Bukkowareteiru")
	}
	if got := res.ContentKind; got != "SPECIAL" {
		t.Fatalf("content_kind=%q want=%q", got, "SPECIAL")
	}
	if got := canonicalSPKind(res); got != "SP" {
		t.Fatalf("sp_kind=%q want=%q", got, "SP")
	}
	if sem := canonicalEPSemantics(res); sem == nil {
		t.Fatalf("ep_semantics should be promoted for dash SP before tech paren")
	}
	if got := canonicalEPKind(res); got != "SP" {
		t.Fatalf("ep_semantics.kind=%q want=SP", got)
	}
	if got := canonicalSPKind(res); got != "SP" {
		t.Fatalf("sp_kind=%q want=SP", got)
	}
}

func TestParserSpecial_EnglishSpecialTailMarkerPromotesSpecial(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Erai-raws] Isekai de Cheat Skill o Te ni Shita Ore wa, Genjitsu Sekai o mo Musou Suru: Level Up wa Jinsei o Kaeta Special - Special [1080p CR WEBRip HEVC AAC][MultiSub][7FFDB43A]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalTitle(res); got != "Isekai de Cheat Skill o Te ni Shita Ore wa, Genjitsu Sekai o mo Musou Suru : Level Up wa Jinsei o Kaeta Special" {
		t.Fatalf("title=%q", got)
	}
	if got := res.ContentKind; got != "SPECIAL" {
		t.Fatalf("content_kind=%q want=%q", got, "SPECIAL")
	}
	if got := canonicalSPKind(res); got != "SP" {
		t.Fatalf("sp_kind=%q want=%q", got, "SP")
	}
	if sem := canonicalEPSemantics(res); sem == nil {
		t.Fatalf("ep_semantics should not be nil")
	}
	if got := canonicalEPKind(res); got != "SP" {
		t.Fatalf("ep_semantics.kind=%q want=SP", got)
	}
}

func TestParserSpecial_SeasonDashSPDoesNotBecomeSeasonEpisodeEcho(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[黒ネズミたち] 杖与剑的魔剑谭 第二季 / Tsue to Tsurugi no Wistoria Season 2 - SP (B-Global 1920x1080 HEVC AAC MKV)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalTitle(res); got != "杖与剑的魔剑谭" {
		t.Fatalf("title=%q want=%q", got, "杖与剑的魔剑谭")
	}
	if got := res.ContentKind; got != "SPECIAL" {
		t.Fatalf("content_kind=%q want=%q", got, "SPECIAL")
	}
	if got := canonicalSPKind(res); got != "SP" {
		t.Fatalf("sp_kind=%q want=%q", got, "SP")
	}
	if sem := canonicalEPSemantics(res); sem == nil {
		t.Fatalf("expected ep_semantics")
	} else {
		if got := canonicalEPKind(res); got != "SP" {
			t.Fatalf("ep_semantics.kind=%q want=%q", got, "SP")
		}
		if sem.Primary != nil && strings.TrimSpace(sem.Primary.Episode) != "" {
			t.Fatalf("special-only sample should not emit episode=%q", sem.Primary.Episode)
		}
	}
	for _, a := range canonicalAliases(res) {
		if strings.Contains(strings.ToUpper(strings.TrimSpace(a)), " SP") || strings.EqualFold(strings.TrimSpace(a), "SP") {
			t.Fatalf("special tail should be trimmed from alias, aliases=%v", canonicalAliases(res))
		}
	}
}

func TestParserEPSlotAxis_BracketEpisodeUsesABS(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Group] Sample Title [07][1080p][WEB-DL]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if sem := canonicalEPSemantics(res); sem == nil {
		t.Fatalf("ep_semantics should not be nil")
	}
	if got := canonicalEPAxis(res); got != "ABS" {
		t.Fatalf("ep_semantics.axis=%q want=%q", got, "ABS")
	}
}

func TestParserEPSlotAxis_EpisodeKeywordUsesABS(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Group] Sample Title - Episode 52 [1080p][WEB-DL]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if sem := canonicalEPSemantics(res); sem == nil {
		t.Fatalf("ep_semantics should not be nil")
	}
	if got := canonicalEPAxis(res); got != "ABS" {
		t.Fatalf("ep_semantics.axis=%q want=%q", got, "ABS")
	}
}

func TestParserEPSlotAxis_DashEpisodeBeforeTechBracketUsesABS(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Group] Sample Title - 07 [1080p][WEB-DL]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if sem := canonicalEPSemantics(res); sem == nil {
		t.Fatalf("ep_semantics should not be nil")
	}
	if got := canonicalEPAxis(res); got != "ABS" {
		t.Fatalf("ep_semantics.axis=%q want=%q", got, "ABS")
	}
}

func TestParserEPSlotAxis_EPrefixedTokenUsesABS(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Group] Sample Title E10 [1080p][WEB-DL]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if sem := canonicalEPSemantics(res); sem == nil {
		t.Fatalf("ep_semantics should not be nil")
	}
	if got := canonicalEPAxis(res); got != "ABS" {
		t.Fatalf("ep_semantics.axis=%q want=%q", got, "ABS")
	}
}

func TestParserEPSlotAxis_RangeUsesABSWithoutSeason(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Group] Sample Title [01-12][1080p][WEB-DL]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if sem := canonicalEPSemantics(res); sem == nil {
		t.Fatalf("ep_semantics should not be nil")
	}
	if got := canonicalEPKind(res); got != "RANGE" {
		t.Fatalf("ep_semantics.kind=%q want=%q", got, "RANGE")
	}
	if got := canonicalEPAxis(res); got != "ABS" {
		t.Fatalf("ep_semantics.axis=%q want=%q", got, "ABS")
	}
}

func TestParserEPSlotAxis_GroupRouteOverrideUsesABS(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[丸子家族][我们这一家 NEXT(Atashin'Chi NEXT)][01]柚子的活动铅笔[2024.06.05][简日][1080P][MP4]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if sem := canonicalEPSemantics(res); sem == nil {
		t.Fatalf("ep_semantics should not be nil")
	}
	if got := canonicalEPSource(res); got != "group_route_override" {
		t.Fatalf("ep_semantics.source=%q want=%q", got, "group_route_override")
	}
	if got := canonicalEPAxis(res); got != "ABS" {
		t.Fatalf("ep_semantics.axis=%q want=%q", got, "ABS")
	}
}

func TestParserEpisodeRange_EPrefixedRangeDetected(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "Boruto S01 E01-E192 1080p WEB-DL H264 AAC 中文字幕"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalEpisodeRange(res); got != "1-192" {
		t.Fatalf("episode_range=%q want=%q", got, "1-192")
	}
	if got := canonicalEpisode(res); got != "" {
		t.Fatalf("episode should be empty when range exists, got=%q", got)
	}
	if got := canonicalSeason(res); got != "1" {
		t.Fatalf("season=%q want=%q", got, "1")
	}
	if got := canonicalGroup(res); got != "" {
		t.Fatalf("group should stay empty for no-group title, got=%q", got)
	}
	if got := firstAttrValue(res, model.AttrTailGroup); got != "" {
		t.Fatalf("tail_group should be empty for e-prefixed range token, got=%q", got)
	}
}

func TestParserEpisode_DecimalChineseEpisodeDetected(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[AngelEcho]自称Fランクのお兄さまがゲームで評価される学園の頂点に君臨するそうですよ？ [第26.5话]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalEpisode(res); got != "26.5" {
		t.Fatalf("episode=%q want=%q", got, "26.5")
	}
}

func TestParserEpisode_BracketCatalogIDNotMisreadAsEpisode(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[PMR] [BDMV] Haite Kudasai, Takamine-san [Vol.3] [KAXA-9023]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalEpisode(res); got == "9023" {
		t.Fatalf("catalog id should not be parsed as episode, got=%q", got)
	}
}

func TestParserEpisode_SeasonNumberInAliasNotMisreadAsEpisode(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[FY-Raws]赛马娘 第三季 [Uma Musume ~Pretty Derby~ 3][ウマ娘 プリティーダービー Season 3][BDRip][1080P_x265(10bit)-FLAC][ALL]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalEpisode(res); got == "3" {
		t.Fatalf("season number in alias must not be parsed as episode, got=%q", got)
	}
	if got := canonicalSeason(res); got != "3" {
		t.Fatalf("season=%q want=%q", got, "3")
	}
}

func TestParserEpisode_VolumeRangeNotMisreadAsEpisode(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[PMR]Re:从零开始的异世界生活 第三季/ [BDMV] Re Zero kara Hajimeru Isekai Seikatsu 3rd Season [Vol.1-Vol.5]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalEpisode(res); got == "1" || got == "5" {
		t.Fatalf("volume range number must not be parsed as episode, got=%q", got)
	}
}

func TestParserTitle_PreEpisodeTokenDoesNotBlankTitle(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[超次元ゲイム ネプテューヌ][简体字幕]OVA3 沐浴阳光的紫茄子花 陽だまりの茄花[超次元游戏海王星][Hyperdimension Neptunia][CHS]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	title := canonicalTitle(res)
	if !strings.Contains(title, "沐浴阳光的紫茄子花") {
		t.Fatalf("pre-episode token should not blank title, got=%q", title)
	}
	if got := canonicalEpisode(res); got != "3" {
		t.Fatalf("episode=%q want=%q", got, "3")
	}
}

func TestParserGroup_LeadingMetaBracketThenGroupDetected(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[4K][DBD-Raws][少女与战车 最终章 第1话/Girls und Panzer das Finale 01][2160P][BDRip][HEVC-10bit][简繁外挂][FLACx4][MKV](ガールズ&パンツァー 最終章 第1話)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalGroup(res); got != "DBD-Raws" {
		t.Fatalf("group=%q want=%q", got, "DBD-Raws")
	}
	if got := canonicalTitle(res); !strings.Contains(got, "少女与战车") {
		t.Fatalf("title should keep main title, got=%q", got)
	}
}

func TestParserGroup_LeadingVersionMetaThenGroupDetected(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[V2修正版][夜莺家族&YYQ字幕组]New Doraemon 哆啦A梦新番[707][2022.05.21][AVC][1080P][GB_JP]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := strings.ReplaceAll(canonicalGroup(res), " ", ""); got != "夜莺家族&YYQ字幕组" {
		t.Fatalf("group(normalized)=%q want=%q", got, "夜莺家族&YYQ字幕组")
	}
	if got := canonicalTitle(res); !strings.Contains(got, "New Doraemon") {
		t.Fatalf("title should keep program title, got=%q", got)
	}
}

func TestParserPlatform_BSExChannelDoesNotTriggerSPKindEX(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[NanakoRaws] Nohara Hiroshi Hiru Meshi no Ryuugi (BS-EX 4K 3840x2160 x265 AAC)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalSPKind(res); got == "EX" {
		t.Fatalf("BS-EX channel marker must not trigger sp_kind=EX, got=%q", got)
	}
	if !hasAttrValue(attrValues(res, model.AttrPlatform), "BS-EX") {
		t.Fatalf("expected platform=BS-EX, attrs=%v", attrValues(res, model.AttrPlatform))
	}
}

func TestParserPlatform_CSExChannelDoesNotTriggerSPKindEX(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[shincaps] Doraemon Birthday Special 2024 (CS-EX1 1440x1080 MPEG2 AAC).ts"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalSPKind(res); got == "EX" {
		t.Fatalf("CS-EX1 channel marker must not trigger sp_kind=EX, got=%q", got)
	}
	if !hasAttrValue(attrValues(res, model.AttrPlatform), "CS-EX1") {
		t.Fatalf("expected platform=CS-EX1, attrs=%v", attrValues(res, model.AttrPlatform))
	}
}

func TestParserLexicon_CommaLanguageTokensAreConsumed(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "A Couple of Cuckoos S02E07-09 MULTi 1080p WEB x264 AAC -Tsundere-Raws (CR) (VF, FRENCH, VOSTFR, Kakkou no Iinazuke Season 2)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	aliases := canonicalAliases(res)
	for _, a := range aliases {
		la := strings.ToLower(a)
		if strings.Contains(la, "vf,") || strings.Contains(la, "french,") || strings.Contains(la, "vostfr,") {
			t.Fatalf("comma language token leaked into alias: %v", aliases)
		}
	}
	if !hasAttrValue(attrValues(res, model.AttrSubFlag), "soft") {
		t.Fatalf("expected sub_flag=soft, attrs=%v", attrValues(res, model.AttrSubFlag))
	}
}

func TestParserFrenchSignals_VfVostfrAreStructured(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "Aishite Knight (Lucille, amour et rock 'n roll) MULTI DVD CUSTOM VF/VOSTFR"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !hasAttrValue(attrValues(res, model.AttrAudioLang), "FRA") {
		t.Fatalf("expected audio_lang=FRA, got=%v", attrValues(res, model.AttrAudioLang))
	}
	if !hasAttrValue(attrValues(res, model.AttrAudioFlag), "DUB") {
		t.Fatalf("expected audio_flag=DUB, got=%v", attrValues(res, model.AttrAudioFlag))
	}
	if !hasAttrValue(attrValues(res, model.AttrSubFlag), "soft") {
		t.Fatalf("expected sub_flag=soft, got=%v", attrValues(res, model.AttrSubFlag))
	}
	if !hasAttrValue(attrValues(res, model.AttrSubLang), "FRA") {
		t.Fatalf("expected sub_lang=FRA, got=%v", attrValues(res, model.AttrSubLang))
	}
}

func TestParserFrenchSignals_TrueFrenchStructured(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "Pokemon.Film.01.Mewtwo.contre.attaque.1998.TRUEFRENCH.DVDISO.576p.MPEG.FranceTV-MaitreAnonyme.iso"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !hasAttrValue(attrValues(res, model.AttrAudioLang), "FRA") {
		t.Fatalf("expected audio_lang=FRA, got=%v", attrValues(res, model.AttrAudioLang))
	}
	if !hasAttrValue(attrValues(res, model.AttrAudioFlag), "DUB") {
		t.Fatalf("expected audio_flag=DUB, got=%v", attrValues(res, model.AttrAudioFlag))
	}
}

func TestParserFrenchSignals_FrenchSubtitleStructured(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[TestGroup] Example Show [法文字幕][1080p][WEB-DL]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !hasAttrValue(attrValues(res, model.AttrSubLang), "FRA") {
		t.Fatalf("expected sub_lang=FRA, got=%v", attrValues(res, model.AttrSubLang))
	}
	if !hasAttrValue(attrValues(res, model.AttrSubFlag), "soft") {
		t.Fatalf("expected sub_flag=soft, got=%v", attrValues(res, model.AttrSubFlag))
	}
}

func TestParserTailParenAlias_CommaSeparatedListSplit(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "Dragon Raja -The Blazing Dawn- (Japanese ver.) S02E01 VOSTFR 1080p WEB x264 AAC -Tsundere-Raws (CR) (Long Zu II: Daowangzhe Zhi Tong,Dragon Raja II: The Mourner's Eyes)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	aliases := canonicalAliases(res)
	if len(aliases) < 2 {
		t.Fatalf("expected split aliases, got=%v", aliases)
	}
	foundLongZu := false
	foundDragonRaja := false
	for _, a := range aliases {
		la := strings.ToLower(a)
		if strings.Contains(la, "long zu ii") {
			foundLongZu = true
		}
		if strings.Contains(la, "dragon raja ii") {
			foundDragonRaja = true
		}
		if strings.Contains(a, ",") {
			t.Fatalf("comma list should be split into separate aliases, got=%v", aliases)
		}
	}
	if !foundLongZu || !foundDragonRaja {
		t.Fatalf("missing expected split aliases, got=%v", aliases)
	}
}

func TestParserTailParenAlias_TokensConsumedAfterExtraction(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "A Star Brighter Than the Sun S01E01 MULTi 1080p WEB H.264 E-AC-3 -Tsundere-Raws (AMZN) (VF, FRENCH, VOSTFR, Taiyou yori mo Mabushii Hoshi)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	for _, tok := range res.Tokens {
		if strings.EqualFold(strings.TrimSpace(tok.Text), "yori") && !tok.Removed {
			t.Fatalf("tail paren alias token should be consumed, tokens=%v", res.Tokens)
		}
	}
	if !hasAttrValue(attrValues(res, model.AttrAudioCodec), "EAC3") {
		t.Fatalf("expected audio_codec=EAC3 from dashed token, got=%v", attrValues(res, model.AttrAudioCodec))
	}
	for _, tok := range res.Tokens {
		txt := strings.ToUpper(strings.TrimSpace(tok.Text))
		if (txt == "WEB" || txt == "E" || txt == "AC" || txt == "3") && !tok.Removed {
			t.Fatalf("tech token should be consumed in post-process, token=%q", tok.Text)
		}
	}
}

func TestParserStandaloneWebSource_InferredAndConsumed(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "100 METERS (2025) MULTi 1080p WEB AV1 E-AC-3 -Tsundere-Raws (NF) (VF, FRENCH, VOSTFR, Hyakuemu.)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !hasAttrValue(attrValues(res, model.AttrSourceType), "WEBRip") {
		t.Fatalf("expected source_type=WEBRip, got=%v", attrValues(res, model.AttrSourceType))
	}
	if !hasAttrValue(attrValues(res, model.AttrAudioCodec), "EAC3") {
		t.Fatalf("expected audio_codec=EAC3, got=%v", attrValues(res, model.AttrAudioCodec))
	}
	for _, tok := range res.Tokens {
		if strings.EqualFold(strings.TrimSpace(tok.Text), "WEB") && !tok.Removed {
			t.Fatalf("standalone WEB token should be consumed, tokens=%v", res.Tokens)
		}
	}
}

func TestParserStandaloneDVDSource_InferredAndConsumed(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "Aishite Knight (Lucille, amour et rock 'n roll) MULTI DVD CUSTOM VF/VOSTFR"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !hasAttrValue(attrValues(res, model.AttrSourceType), "DVD") {
		t.Fatalf("expected source_type=DVD, got=%v", attrValues(res, model.AttrSourceType))
	}
	if !hasAttrValue(attrValues(res, model.AttrEdition), "CUSTOM") {
		t.Fatalf("expected edition=CUSTOM, got=%v", attrValues(res, model.AttrEdition))
	}
	if hasAttrValue(attrValues(res, model.AttrNote), "CUSTOM") {
		t.Fatalf("CUSTOM should not remain in note, notes=%v", attrValues(res, model.AttrNote))
	}
	for _, tok := range res.Tokens {
		if strings.EqualFold(strings.TrimSpace(tok.Text), "DVD") && !tok.Removed {
			t.Fatalf("standalone DVD token should be consumed, tokens=%v", res.Tokens)
		}
	}
}

func TestParserStandaloneDVDSource_RemuxTail(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "(SKMD) Oh My Goddess-The Movie DVD Remux"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !hasAttrValue(attrValues(res, model.AttrSourceType), "DVD") {
		t.Fatalf("expected source_type=DVD, got=%v", attrValues(res, model.AttrSourceType))
	}
	for _, tok := range res.Tokens {
		if strings.EqualFold(strings.TrimSpace(tok.Text), "DVD") && !tok.Removed {
			t.Fatalf("DVD token should be consumed in remux tail, tokens=%v", res.Tokens)
		}
	}
}

func TestParserStandaloneDVDSource_ReideenDVDISOsConsumed(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "Reideen DVD-ISOs (2014 US DVD)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !hasAttrValue(attrValues(res, model.AttrSourceType), "DVD") {
		t.Fatalf("expected source_type=DVD, got=%v", attrValues(res, model.AttrSourceType))
	}
	for _, tok := range res.Tokens {
		low := strings.ToLower(strings.TrimSpace(tok.Text))
		if (low == "dvd" || low == "isos") && !tok.Removed {
			t.Fatalf("dvd-iso span should be consumed, tokens=%v", res.Tokens)
		}
	}
}

func TestParserLexicon_MediaToken_VHSBracketConsumed(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := `Motto! Ojamajo Doremi - "Together with Mama" Tell me ♪ Momo-chan Snack Cooking [VHS]`
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !hasAttrValue(attrValues(res, model.AttrSourceType), "VHS") {
		t.Fatalf("expected source_type=VHS, got=%v", attrValues(res, model.AttrSourceType))
	}
	if containsAlias(canonicalAliases(res), "VHS") {
		t.Fatalf("vhs marker should not stay in alias, aliases=%v", canonicalAliases(res))
	}
	for _, tok := range res.Tokens {
		if strings.EqualFold(strings.TrimSpace(tok.Text), "VHS") && !tok.Removed {
			t.Fatalf("vhs token should be consumed, tokens=%v", res.Tokens)
		}
	}
}

func TestParserLexicon_MediaToken_R2JDVDISOConsumed(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "Slam Dunk R2J DVD_ISO"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !hasAttrValue(attrValues(res, model.AttrSourceType), "DVD") {
		t.Fatalf("expected source_type=DVD, got=%v", attrValues(res, model.AttrSourceType))
	}
	if !hasAttrValue(attrValues(res, model.AttrPlatformRegion), "JP") {
		t.Fatalf("expected platform_region=JP, got=%v", attrValues(res, model.AttrPlatformRegion))
	}
	if !hasAttrValue(attrValues(res, model.AttrEdition), "DVD_ISO") {
		t.Fatalf("expected edition=DVD_ISO, got=%v", attrValues(res, model.AttrEdition))
	}
	if !hasAttrValue(attrValues(res, model.AttrEdition), "R2J") {
		t.Fatalf("expected edition=R2J, got=%v", attrValues(res, model.AttrEdition))
	}
	if hasAttrValue(attrValues(res, model.AttrNote), "DVD_ISO") {
		t.Fatalf("DVD_ISO should not remain in note, notes=%v", attrValues(res, model.AttrNote))
	}
	if hasAttrValue(attrValues(res, model.AttrNote), "R2J") {
		t.Fatalf("R2J should not remain in note, notes=%v", attrValues(res, model.AttrNote))
	}
	if title := strings.ToLower(canonicalTitle(res)); strings.Contains(title, "iso") {
		t.Fatalf("iso residual should not leak into title, title=%q", canonicalTitle(res))
	}
	for _, tok := range res.Tokens {
		txt := strings.ToLower(strings.TrimSpace(tok.Text))
		if (txt == "dvd" || txt == "iso") && !tok.Removed {
			t.Fatalf("dvd/iso tokens should be consumed, tokens=%v", res.Tokens)
		}
	}
}

func TestParserLexicon_MediaToken_BDMVPromotedToEdition(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[BDMV] CITY THE ANIMATION Vol.1-2"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !hasAttrValue(attrValues(res, model.AttrEdition), "BDMV") {
		t.Fatalf("expected edition=BDMV, got=%v", attrValues(res, model.AttrEdition))
	}
	if hasAttrValue(attrValues(res, model.AttrNote), "BDMV") {
		t.Fatalf("BDMV should not remain in note, notes=%v", attrValues(res, model.AttrNote))
	}
	if !hasAttrValue(attrValues(res, model.AttrSourceType), "BD") {
		t.Fatalf("expected source_type=BD, got=%v", attrValues(res, model.AttrSourceType))
	}
}

func TestParserLexicon_MediaToken_USBDJPBDConsumed(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	cases := []struct {
		raw      string
		marker   string
		region   string
		note     string
		titleHit string
	}{
		{
			raw:      "Ultraman The Adventure Begins USBD",
			marker:   "USBD",
			region:   "US",
			note:     "USBD",
			titleHit: "USBD",
		},
		{
			raw:      "Ultraman USA- The Adventure Begins JPBD",
			marker:   "JPBD",
			region:   "JP",
			note:     "JPBD",
			titleHit: "JPBD",
		},
	}
	for _, tc := range cases {
		res, err := p.Parse(tc.raw)
		if err != nil {
			t.Fatalf("parse failed for %q: %v", tc.raw, err)
		}
		if !hasAttrValue(attrValues(res, model.AttrSourceType), "BD") {
			t.Fatalf("expected source_type=BD for %q, got=%v", tc.raw, attrValues(res, model.AttrSourceType))
		}
		if !hasAttrValue(attrValues(res, model.AttrPlatformRegion), tc.region) {
			t.Fatalf("expected platform_region=%s for %q, got=%v", tc.region, tc.raw, attrValues(res, model.AttrPlatformRegion))
		}
		if !hasAttrValue(attrValues(res, model.AttrNote), tc.note) {
			t.Fatalf("expected note=%s for %q, got=%v", tc.note, tc.raw, attrValues(res, model.AttrNote))
		}
		if strings.Contains(strings.ToUpper(canonicalTitle(res)), tc.titleHit) {
			t.Fatalf("marker should not leak into title for %q, title=%q", tc.raw, canonicalTitle(res))
		}
		for _, tok := range res.Tokens {
			if strings.EqualFold(strings.TrimSpace(tok.Text), tc.marker) && !tok.Removed {
				t.Fatalf("%s token should be consumed for %q, tokens=%v", tc.marker, tc.raw, res.Tokens)
			}
		}
	}
}

func TestParserLexicon_MediaToken_RetailBootlegConsumed(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	cases := []struct {
		raw       string
		note      string
		forbidden string
	}{
		{
			raw:       "Dragon Ball (TV Series 1986-1989) DVD9 RETAIL",
			note:      "RETAIL",
			forbidden: "RETAIL",
		},
		{
			raw:       "Jetter Mars / Marte O Menino Bionico Português Brasileiro Bootleg DVD",
			note:      "BOOTLEG",
			forbidden: "BOOTLEG",
		},
	}
	for _, tc := range cases {
		res, err := p.Parse(tc.raw)
		if err != nil {
			t.Fatalf("parse failed for %q: %v", tc.raw, err)
		}
		if !hasAttrValue(attrValues(res, model.AttrNote), tc.note) {
			t.Fatalf("expected note=%s for %q, got=%v", tc.note, tc.raw, attrValues(res, model.AttrNote))
		}
		if strings.Contains(strings.ToUpper(canonicalTitle(res)), tc.forbidden) {
			t.Fatalf("%s should not stay in title for %q, title=%q", tc.forbidden, tc.raw, canonicalTitle(res))
		}
	}
}

func TestParserLexicon_MediaToken_LaserDiscBoxConsumed(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "Fushigi no Umi no Nadia episode Previews exclusive to the Laser Disc Box v2"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !hasAttrValue(attrValues(res, model.AttrSourceType), "LD") {
		t.Fatalf("expected source_type=LD, got=%v", attrValues(res, model.AttrSourceType))
	}
	if !hasAttrValue(attrValues(res, model.AttrEdition), "LASER_DISC_BOX") {
		t.Fatalf("expected edition=LASER_DISC_BOX, got=%v", attrValues(res, model.AttrEdition))
	}
	if hasAttrValue(attrValues(res, model.AttrNote), "LASER_DISC_BOX") {
		t.Fatalf("LASER_DISC_BOX should not remain in note, notes=%v", attrValues(res, model.AttrNote))
	}
	if title := strings.ToLower(canonicalTitle(res)); strings.Contains(title, "laser disc box") {
		t.Fatalf("laser disc box should be consumed from title, got=%q", canonicalTitle(res))
	}
}

func TestParserLexicon_LangMarker_RusBracketConsumed(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "City Hunter 1987 [Rus]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if title := canonicalTitle(res); strings.Contains(strings.ToLower(title), "rus") {
		t.Fatalf("rus marker should not leak into title, got=%q", title)
	}
	if !hasAttrValue(attrValues(res, model.AttrAudioLang), "RUS") {
		t.Fatalf("expected audio_lang=RUS, got=%v", attrValues(res, model.AttrAudioLang))
	}
	if hasAttrValue(attrValues(res, model.AttrNote), "RUS") {
		t.Fatalf("RUS should not remain in note, notes=%v", attrValues(res, model.AttrNote))
	}
	for _, tok := range res.Tokens {
		if strings.EqualFold(strings.TrimSpace(tok.Text), "Rus") && !tok.Removed {
			t.Fatalf("rus token should be consumed, tokens=%v", res.Tokens)
		}
	}
}

func TestParserLexicon_LangTagLatFreJapSubsStructured(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[A&C] 足球小将.日语+西班牙语+法语.英文+法文.Captain Tsubasa (2018) - [01-14] [LAT-FRE-JAP+Subs] [BDrip1080p]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !hasAttrValue(attrValues(res, model.AttrAudioLang), "SPA") ||
		!hasAttrValue(attrValues(res, model.AttrAudioLang), "FRA") ||
		!hasAttrValue(attrValues(res, model.AttrAudioLang), "JPN") {
		t.Fatalf("expected audio_lang SPA/FRA/JPN, got=%v", attrValues(res, model.AttrAudioLang))
	}
	if !hasAttrValue(attrValues(res, model.AttrAudioFlag), "MULTI_AUDIO") {
		t.Fatalf("expected audio_flag=MULTI_AUDIO, got=%v", attrValues(res, model.AttrAudioFlag))
	}
	if !hasAttrValue(attrValues(res, model.AttrSubFlag), "soft") {
		t.Fatalf("expected sub_flag=soft, got=%v", attrValues(res, model.AttrSubFlag))
	}
	if hasAttrValue(attrValues(res, model.AttrNote), "LAT-FRE-JAP+Subs") {
		t.Fatalf("LAT-FRE-JAP+Subs should not remain in note, notes=%v", attrValues(res, model.AttrNote))
	}
}

func TestParserLexicon_LangMarker_CHNStructured(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "Hui Buh Das Schlossgespenst 2006 1080p CHN Blu-ray AVC DTS 5.1-bury"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !hasAttrValue(attrValues(res, model.AttrAudioLang), "CMN") {
		t.Fatalf("expected audio_lang=CMN, got=%v", attrValues(res, model.AttrAudioLang))
	}
	if hasAttrValue(attrValues(res, model.AttrNote), "CHN") {
		t.Fatalf("CHN should not remain in note, notes=%v", attrValues(res, model.AttrNote))
	}
}

func TestParserLexicon_LangMarker_EspanoloParenConsumed(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "Zetman (Españolo)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	aliases := canonicalAliases(res)
	for _, a := range aliases {
		la := strings.ToLower(strings.TrimSpace(a))
		if strings.Contains(la, "español") || strings.Contains(la, "espanol") {
			t.Fatalf("espanol marker should not stay in alias, aliases=%v", aliases)
		}
	}
}

func TestParserLexicon_LangMarker_SubEspanolStructured(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "Silent Witch: Chinmoku no Majo no Kakushigoto S01 Sub Español [1080p]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !hasAttrValue(attrValues(res, model.AttrSubFlag), "soft") {
		t.Fatalf("expected sub_flag=soft, got=%v", attrValues(res, model.AttrSubFlag))
	}
	if !hasAttrValue(attrValues(res, model.AttrSubLang), "SPA") {
		t.Fatalf("expected sub_lang=SPA, got=%v", attrValues(res, model.AttrSubLang))
	}
	if hasAttrValue(attrValues(res, model.AttrNote), "ESP") {
		t.Fatalf("ESP should not remain in note, notes=%v", attrValues(res, model.AttrNote))
	}
}

func TestParserLexicon_LangMarker_EspanolLatinoStructured(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	cases := []string{
		"[Tento] Yu-Gi-Oh! SEVENS [720p] (Doblaje Español Latino)",
		"Dr. Slump 1997 [480] Español Latino",
	}
	for _, raw := range cases {
		res, err := p.Parse(raw)
		if err != nil {
			t.Fatalf("parse failed for %q: %v", raw, err)
		}
		if !hasAttrValue(attrValues(res, model.AttrAudioLang), "SPA") {
			t.Fatalf("expected audio_lang=SPA for %q, got=%v", raw, attrValues(res, model.AttrAudioLang))
		}
		if hasAttrValue(attrValues(res, model.AttrNote), "ESP") {
			t.Fatalf("ESP should not remain in note for %q, notes=%v", raw, attrValues(res, model.AttrNote))
		}
	}
}

func TestParserStandaloneDVDSource_TokutenDVDSuffixConsumed(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Moozzi2] 咒术回战 第二季/Jujutsu Kaisen 2nd Season (BD 1920x1080 x265-10Bit Flac) - TV + Tokuten DVD"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	title := canonicalTitle(res)
	if strings.Contains(strings.ToLower(title), "dvd") {
		t.Fatalf("tokuten dvd suffix should not leak into title: %q", title)
	}
	for _, tok := range res.Tokens {
		if strings.EqualFold(strings.TrimSpace(tok.Text), "DVD") && !tok.Removed {
			t.Fatalf("tokuten dvd token should be consumed, tokens=%v", res.Tokens)
		}
	}
}

func TestParserStandaloneAudioCodec_FLACInTechContext(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "I Got a Cheat Skill in Another World and Became Unrivaled in The Real World, Too S01E13 German DL FLAC 1080p BluRay x264"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !hasAttrValue(attrValues(res, model.AttrAudioCodec), "FLAC") {
		t.Fatalf("expected audio_codec=FLAC, got=%v", attrValues(res, model.AttrAudioCodec))
	}
	for _, tok := range res.Tokens {
		if strings.EqualFold(strings.TrimSpace(tok.Text), "FLAC") && !tok.Removed {
			t.Fatalf("standalone FLAC token should be consumed, tokens=%v", res.Tokens)
		}
	}
}

func TestParserStandaloneAudioCodec_TrueHDBracket(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[anime4life.] [2016] Kizumonogatari Part 1: Tekketsu [1080p] [BluRay] [x264] [Dolby TrueHD] [Dual Audio]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !hasAttrValue(attrValues(res, model.AttrAudioCodec), "TRUEHD") {
		t.Fatalf("expected audio_codec=TRUEHD, got=%v", attrValues(res, model.AttrAudioCodec))
	}
	for _, tok := range res.Tokens {
		if strings.EqualFold(strings.TrimSpace(tok.Text), "TrueHD") && !tok.Removed {
			t.Fatalf("standalone TrueHD token should be consumed, tokens=%v", res.Tokens)
		}
	}
}

func TestParserStandaloneAudioCodec_DTSHDMABracket(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Niconeiko Works] Colorful Stage! The Movie A Miku Who Can`t Sing [Ma10p] [FLAC] [DTS-HDMA]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !hasAttrValue(attrValues(res, model.AttrAudioCodec), "FLAC") {
		t.Fatalf("expected audio_codec=FLAC, got=%v", attrValues(res, model.AttrAudioCodec))
	}
	if !hasAttrValue(attrValues(res, model.AttrAudioCodec), "DTSHDMA") && !hasAttrValue(attrValues(res, model.AttrAudioCodec), "DTS-HDMA") {
		t.Fatalf("expected audio_codec DTSHDMA, got=%v", attrValues(res, model.AttrAudioCodec))
	}
	if hasAttrValue(attrValues(res, model.AttrAudioCodec), "DTS") {
		t.Fatalf("base DTS should be collapsed when DTSHDMA exists, got=%v", attrValues(res, model.AttrAudioCodec))
	}
	if res.AudioSlot != nil {
		hasDTSHDMA := false
		for _, tr := range res.AudioSlot.Tracks {
			if strings.EqualFold(strings.TrimSpace(tr.Codec), "DTS") {
				t.Fatalf("audio_slot should drop base DTS when DTSHDMA exists, tracks=%+v", res.AudioSlot.Tracks)
			}
			if strings.EqualFold(strings.TrimSpace(tr.Codec), "DTSHDMA") || strings.EqualFold(strings.TrimSpace(tr.Codec), "DTS-HDMA") {
				hasDTSHDMA = true
			}
		}
		if !hasDTSHDMA {
			t.Fatalf("audio_slot should contain DTSHDMA track, tracks=%+v", res.AudioSlot.Tracks)
		}
	}
	for _, tok := range res.Tokens {
		if strings.EqualFold(strings.TrimSpace(tok.Text), "DTS-HDMA") && !tok.Removed {
			t.Fatalf("standalone DTS-HDMA token should be consumed, tokens=%v", res.Tokens)
		}
	}
}

func TestParserStandaloneAudioCodec_Ma10pFLACTailConsumed(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Niconeiko Works] O.N.G.E.K.I. 音击 LIVE Vol.1 Jump!! Jump!! Jump!! Ma10p_FLAC"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	title := canonicalTitle(res)
	aliases := canonicalAliases(res)
	if strings.Contains(strings.ToLower(title), "ma10p") || strings.Contains(strings.ToLower(title), "flac") {
		t.Fatalf("ma10p/flac should not leak into title: %q", title)
	}
	for _, a := range aliases {
		la := strings.ToLower(a)
		if strings.Contains(la, "ma10p") || strings.Contains(la, "flac") {
			t.Fatalf("ma10p/flac should not leak into aliases: %v", aliases)
		}
	}
	for _, tok := range res.Tokens {
		low := strings.ToLower(strings.TrimSpace(tok.Text))
		if (low == "ma10p" || low == "_" || low == "flac") && !tok.Removed {
			t.Fatalf("ma10p_flac tokens should be consumed, tokens=%v", res.Tokens)
		}
	}
}

func TestParserStandaloneBitDepth_Ma10pTailConsumed(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Niconeiko Works] Adachi to Shimamura 安达与岛村 1080P BDRip Ma10p FLAC"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !hasAttrValue(attrValues(res, model.AttrBitDepth), "10bit") {
		t.Fatalf("expected bit_depth=10bit, got=%v", attrValues(res, model.AttrBitDepth))
	}
	for _, tok := range res.Tokens {
		if strings.EqualFold(strings.TrimSpace(tok.Text), "Ma10p") && !tok.Removed {
			t.Fatalf("ma10p token should be consumed, tokens=%v", res.Tokens)
		}
	}
}

func TestParserPreEpisodePipeAliases_AreExtractedAndConsumed(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "Ansatsusha de Aru Ore no Status ga Yuusha yori mo Akiraka ni Tsuyoi Nodaga | My Status as an Assassin Obviously Exceeds the Hero's | Мoй стaтус убийцы oчeвиднo прeвoсхoдит гeрoйский [2025] [01 of 12] [WEBRip] [1080p] [RUS + JAP]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	aliases := canonicalAliases(res)
	if len(aliases) < 2 {
		t.Fatalf("expected pipe aliases extracted, got=%v", aliases)
	}
	if !containsAlias(aliases, "My Status as an Assassin Obviously Exceeds the Hero's") {
		t.Fatalf("missing english alias, got=%v", aliases)
	}
	hasNonLatinAlias := false
	for _, a := range aliases {
		for _, r := range a {
			if (r >= 0x0400 && r <= 0x052F) || (r >= 0x2DE0 && r <= 0x2DFF) || (r >= 0xA640 && r <= 0xA69F) {
				hasNonLatinAlias = true
				break
			}
		}
		if hasNonLatinAlias {
			break
		}
	}
	if !hasNonLatinAlias {
		t.Fatalf("missing non-latin alias, got=%v", aliases)
	}
	if !hasAttrValue(attrValues(res, model.AttrAudioLang), "RUS") {
		t.Fatalf("expected audio_lang=RUS, got=%v", attrValues(res, model.AttrAudioLang))
	}
	if !hasAttrValue(attrValues(res, model.AttrAudioLang), "JPN") {
		t.Fatalf("expected audio_lang=JPN, got=%v", attrValues(res, model.AttrAudioLang))
	}
	if !hasAttrValue(attrValues(res, model.AttrAudioFlag), "DUAL_AUDIO") {
		t.Fatalf("expected audio_flag=DUAL_AUDIO, got=%v", attrValues(res, model.AttrAudioFlag))
	}
	if hasAttrValue(attrValues(res, model.AttrNote), "RUS+JAP") {
		t.Fatalf("RUS+JAP should not remain in note, notes=%v", attrValues(res, model.AttrNote))
	}
	for _, tok := range res.Tokens {
		if strings.TrimSpace(tok.Text) == "|" && !tok.Removed {
			t.Fatalf("pipe delimiters should be consumed after alias extraction")
		}
	}
}

func TestParserLexicon_RecruitmentAndBonusMetaTokensAreConsumed(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[1080P][DBD-Raws][龙珠GT/Dragon Ball GT][01-12TV全集+SP+特典映像][HEVC-10bit][招募翻譯]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	aliases := canonicalAliases(res)
	for _, a := range aliases {
		if strings.Contains(a, "招募") || strings.Contains(a, "特典映像") {
			t.Fatalf("meta token leaked into alias: %v", aliases)
		}
	}
	if !hasAttrValue(attrValues(res, model.AttrNote), "RECRUITMENT") {
		t.Fatalf("expected note=RECRUITMENT")
	}
	if !hasAttrValue(attrValues(res, model.AttrNote), "BONUS_VIDEO") {
		t.Fatalf("expected note=BONUS_VIDEO")
	}
}

func TestParserLexicon_RecruitmentPreviewAndRefinedMetaTokensAreConsumed(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Group][示例标题/Example Title][01][1080p][先行版本][新组招募][招募校对][急招翻译校对时轴特效][精修版]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	aliases := canonicalAliases(res)
	for _, a := range aliases {
		if strings.Contains(a, "先行版") ||
			strings.Contains(a, "先行版本") ||
			strings.Contains(a, "新组招募") ||
			strings.Contains(a, "招募校对") ||
			strings.Contains(a, "急招翻译校对时轴特效") ||
			strings.Contains(a, "精修版") {
			t.Fatalf("meta token leaked into alias: %v", aliases)
		}
	}
	notes := attrValues(res, model.AttrNote)
	if !hasAttrValue(notes, "RECRUITMENT") {
		t.Fatalf("expected note=RECRUITMENT, got=%v", notes)
	}
	if !hasAttrValue(notes, "PREVIEW_VERSION") {
		t.Fatalf("expected note=PREVIEW_VERSION, got=%v", notes)
	}
	if !hasAttrValue(notes, "REFINED_VERSION") {
		t.Fatalf("expected note=REFINED_VERSION, got=%v", notes)
	}
}

func TestParserLexicon_EndAndDigitalRemasterMetaTokensAreConsumed(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Group][示例标题/Example Title][12][完][數碼畫質版][1080p][WEB-DL][AAC AVC]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	aliases := canonicalAliases(res)
	for _, a := range aliases {
		if strings.Contains(a, "完") || strings.Contains(a, "數碼畫質版") || strings.Contains(a, "数码画质版") {
			t.Fatalf("meta token leaked into alias: %v", aliases)
		}
	}
	notes := attrValues(res, model.AttrNote)
	if hasAttrValue(notes, "END") {
		t.Fatalf("END should not remain in note after release_status promotion, notes=%v", notes)
	}
	if !hasAttrValue(attrValues(res, model.AttrReleaseStatus), "END") {
		t.Fatalf("expected release_status=END, got=%v", attrValues(res, model.AttrReleaseStatus))
	}
	if !hasAttrValue(notes, "DIGITAL_REMASTER") {
		t.Fatalf("expected note=DIGITAL_REMASTER, got=%v", notes)
	}
}

func TestParserNotePromotion_1080pToResolution(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "Patema Sottosopra (2013) [1080p.BluRay.x264.FLAC.ITA.JPN.Subs.Ita.Eng] [12.3GB] [stress]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !hasAttrValue(attrValues(res, model.AttrResolution), "1080p") {
		t.Fatalf("expected resolution=1080p, got=%v", attrValues(res, model.AttrResolution))
	}
	if hasAttrValue(attrValues(res, model.AttrNote), "1080p") {
		t.Fatalf("1080p should be promoted from note, notes=%v", attrValues(res, model.AttrNote))
	}
}

func TestParserNotePromotion_AgeRestrictedToContentRating(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[ANi] NUKITASHI 住在拔作島上的我該如何是好？ [年齡限制版] - 11 [1080P][Baha][WEB-DL][AAC AVC][CHT][MP4]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !hasAttrValue(attrValues(res, model.AttrContentRating), "AGE_RESTRICTED") {
		t.Fatalf("expected content_rating=AGE_RESTRICTED, got=%v", attrValues(res, model.AttrContentRating))
	}
	if hasAttrValue(attrValues(res, model.AttrNote), "AGE_RESTRICTED") {
		t.Fatalf("AGE_RESTRICTED should be promoted from note, notes=%v", attrValues(res, model.AttrNote))
	}
}

func TestParserNoteFinalInParenAlias_NoLongerPromotedAsMeta(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "May I Ask for One Final Thing S01E01 MULTi 1080p WEB x264 AAC -Tsundere-Raws (CR) (VF, FRENCH, VOSTFR, Saigo ni Hitotsu dake Onegai shitemo Yoroshii deshou ka, May I Ask for One Final Thing?)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if hasAttrValue(attrValues(res, model.AttrNote), "FINAL") {
		t.Fatalf("paren alias word FINAL should not become note, notes=%v", attrValues(res, model.AttrNote))
	}
	if hasAttrValue(attrValues(res, model.AttrReleaseStatus), "FINAL") {
		t.Fatalf("paren alias word FINAL should not become release_status, got=%v", attrValues(res, model.AttrReleaseStatus))
	}
}

func TestParserNotePromotion_FinalBracketToReleaseStatus(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[TestGroup][Sample Title][12][Final][1080p][WEB-DL]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !hasAttrValue(attrValues(res, model.AttrReleaseStatus), "FINAL") {
		t.Fatalf("expected release_status=FINAL, got=%v", attrValues(res, model.AttrReleaseStatus))
	}
	if hasAttrValue(attrValues(res, model.AttrNote), "FINAL") {
		t.Fatalf("FINAL should be promoted from note, notes=%v", attrValues(res, model.AttrNote))
	}
}

func TestParserNotePromotion_RevToVersion(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Group] Sample Show - 01 [rev] [1080p][WEB-DL]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !hasAttrValue(attrValues(res, model.AttrVersion), "rev") {
		t.Fatalf("expected version=rev, got=%v", attrValues(res, model.AttrVersion))
	}
	if hasAttrValue(attrValues(res, model.AttrNote), "REV") {
		t.Fatalf("REV should be promoted from note, notes=%v", attrValues(res, model.AttrNote))
	}
}

func TestParserLexicon_MonthAndFileshareMetaTokensAreConsumed(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Group][示例标题/Example Title][02][02月新番][招募時軸][服務器種][1080p][WEB-DL]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	aliases := canonicalAliases(res)
	for _, a := range aliases {
		if strings.Contains(a, "02月新番") || strings.Contains(a, "招募時軸") || strings.Contains(a, "服務器種") {
			t.Fatalf("meta token leaked into alias: %v", aliases)
		}
	}
	notes := attrValues(res, model.AttrNote)
	if !hasAttrValue(notes, "02月新番") {
		t.Fatalf("expected note=02月新番, got=%v", notes)
	}
	if !hasAttrValue(notes, "RECRUITMENT") {
		t.Fatalf("expected note=RECRUITMENT, got=%v", notes)
	}
	if !hasAttrValue(notes, "FILESHARE_META") {
		t.Fatalf("expected note=FILESHARE_META, got=%v", notes)
	}
}

func TestParserLexicon_RecruitmentVariantsAndAnnouncementAreConsumed(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Group][示例标题/Example Title][03][招校对][急招后期][招募翻译等][招募轴效][字幕组招人內详][新番速览计划][1080p][WEB-DL]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	aliases := canonicalAliases(res)
	for _, a := range aliases {
		if strings.Contains(a, "招校对") ||
			strings.Contains(a, "急招后期") ||
			strings.Contains(a, "招募翻译等") ||
			strings.Contains(a, "招募轴效") ||
			strings.Contains(a, "字幕组招人內详") ||
			strings.Contains(a, "新番速览计划") {
			t.Fatalf("meta token leaked into alias: %v", aliases)
		}
	}
	notes := attrValues(res, model.AttrNote)
	if !hasAttrValue(notes, "RECRUITMENT") {
		t.Fatalf("expected note=RECRUITMENT, got=%v", notes)
	}
	if !hasAttrValue(notes, "ANNOUNCEMENT") {
		t.Fatalf("expected note=ANNOUNCEMENT, got=%v", notes)
	}
}

func TestParserLexicon_SubAndSearchMetaTokensAreConsumed(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Group][示例标题/Example Title][04][中文字幕][简体中文][特效字幕][日语中字][（檢索][1080p][WEB-DL]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	aliases := canonicalAliases(res)
	for _, a := range aliases {
		if strings.Contains(a, "中文字幕") ||
			strings.Contains(a, "简体中文") ||
			strings.Contains(a, "特效字幕") ||
			strings.Contains(a, "日语中字") ||
			strings.Contains(a, "日語中字") ||
			strings.Contains(a, "（檢索") {
			t.Fatalf("meta token leaked into alias: %v", aliases)
		}
	}
	notes := attrValues(res, model.AttrNote)
	if !hasAttrValue(notes, "FX_SUBS") {
		t.Fatalf("expected note=FX_SUBS, got=%v", notes)
	}
	if !hasAttrValue(notes, "SEARCH_HINT_META") {
		t.Fatalf("expected note=SEARCH_HINT_META, got=%v", notes)
	}
	if !hasAttrValue(attrValues(res, model.AttrSubLang), "ZH") {
		t.Fatalf("expected sub_lang=ZH, got=%v", attrValues(res, model.AttrSubLang))
	}
	if !hasAttrValue(attrValues(res, model.AttrAudioLang), "JPN") {
		t.Fatalf("expected audio_lang=JPN, got=%v", attrValues(res, model.AttrAudioLang))
	}
	if !hasAttrValue(attrValues(res, model.AttrSubFlag), "soft") {
		t.Fatalf("expected sub_flag=soft, got=%v", attrValues(res, model.AttrSubFlag))
	}
	if hasAttrValue(notes, "CHS_SUBS") || hasAttrValue(notes, "JPN_AUDIO_CHS_SUBS") {
		t.Fatalf("CHS/JPN_CHS note should be promoted, notes=%v", notes)
	}
}

func TestParserLexicon_SubtitleStyleAndUncensoredMetaTokensAreConsumed(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Group][示例标题/Example Title][05][国语中字][国粤双语中字][英语中字][无修版][无水印][附外挂字幕][附字幕颜色分角色][合集版][1080p][WEB-DL]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	aliases := canonicalAliases(res)
	for _, a := range aliases {
		if strings.Contains(a, "国语中字") ||
			strings.Contains(a, "国粤双语中字") ||
			strings.Contains(a, "英语中字") ||
			strings.Contains(a, "无修版") ||
			strings.Contains(a, "无水印") ||
			strings.Contains(a, "附外挂字幕") ||
			strings.Contains(a, "附字幕颜色分角色") ||
			strings.Contains(a, "合集版") {
			t.Fatalf("meta token leaked into alias: %v", aliases)
		}
	}
	notes := attrValues(res, model.AttrNote)
	for _, want := range []string{
		"UNCENSORED_VERSION",
		"WATERMARK_FREE",
		"EXTERNAL_SUBS",
		"STYLIZED_SUBS",
	} {
		if !hasAttrValue(notes, want) {
			t.Fatalf("expected note=%s, got=%v", want, notes)
		}
	}
	if !hasAttrValue(attrValues(res, model.AttrPackProfile), "curated_pack") {
		t.Fatalf("expected pack_profile=curated_pack, got=%v", attrValues(res, model.AttrPackProfile))
	}
	if !hasAttrValue(attrValues(res, model.AttrAudioLang), "CMN") {
		t.Fatalf("expected audio_lang=CMN, got=%v", attrValues(res, model.AttrAudioLang))
	}
	if !hasAttrValue(attrValues(res, model.AttrAudioLang), "YUE") {
		t.Fatalf("expected audio_lang=YUE, got=%v", attrValues(res, model.AttrAudioLang))
	}
	if !hasAttrValue(attrValues(res, model.AttrAudioLang), "ENG") {
		t.Fatalf("expected audio_lang=ENG, got=%v", attrValues(res, model.AttrAudioLang))
	}
	if !hasAttrValue(attrValues(res, model.AttrAudioFlag), "DUAL_AUDIO") {
		t.Fatalf("expected audio_flag=DUAL_AUDIO, got=%v", attrValues(res, model.AttrAudioFlag))
	}
	if !hasAttrValue(attrValues(res, model.AttrSubLang), "ZH") {
		t.Fatalf("expected sub_lang=ZH, got=%v", attrValues(res, model.AttrSubLang))
	}
	if !hasAttrValue(attrValues(res, model.AttrSubFlag), "soft") {
		t.Fatalf("expected sub_flag=soft, got=%v", attrValues(res, model.AttrSubFlag))
	}
	if hasAttrValue(notes, "CHN_AUDIO_CHS_SUBS") || hasAttrValue(notes, "ENG_AUDIO_CHS_SUBS") || hasAttrValue(notes, "CHN_YUE_AUDIO_SUBS") {
		t.Fatalf("composite audio/sub notes should be promoted, notes=%v", notes)
	}
	if hasAttrValue(notes, "CURATED_PACK") {
		t.Fatalf("CURATED_PACK should be promoted from note, notes=%v", notes)
	}
}

func TestParserLexicon_HighFpsNoSubAndScheduleMetaTokensAreConsumed(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Group][示例标题/Example Title][06][高帧率][国粤双语][繁中內掛][日语无字][日语简繁中字][急招翻译][（下周停播）][1080p][WEB-DL]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	aliases := canonicalAliases(res)
	for _, a := range aliases {
		if strings.Contains(a, "高帧率") ||
			strings.Contains(a, "国粤双语") ||
			strings.Contains(a, "繁中內掛") ||
			strings.Contains(a, "日语无字") ||
			strings.Contains(a, "日语简繁中字") ||
			strings.Contains(a, "急招翻译") ||
			strings.Contains(a, "下周停播") {
			t.Fatalf("meta token leaked into alias: %v", aliases)
		}
	}
	notes := attrValues(res, model.AttrNote)
	for _, want := range []string{
		"HIGH_FPS",
		"JPN_AUDIO_NO_SUB",
		"JPN_AUDIO_CHS_CHT_SUBS",
		"RECRUITMENT",
		"ANNOUNCEMENT",
	} {
		if !hasAttrValue(notes, want) {
			t.Fatalf("expected note=%s, got=%v", want, notes)
		}
	}
	if !hasAttrValue(attrValues(res, model.AttrAudioLang), "CMN") || !hasAttrValue(attrValues(res, model.AttrAudioLang), "YUE") {
		t.Fatalf("expected audio_lang CMN+YUE, got=%v", attrValues(res, model.AttrAudioLang))
	}
	if !hasAttrValue(attrValues(res, model.AttrAudioFlag), "DUAL_AUDIO") {
		t.Fatalf("expected audio_flag=DUAL_AUDIO, got=%v", attrValues(res, model.AttrAudioFlag))
	}
	if !hasAttrValue(attrValues(res, model.AttrSubLang), "TC") {
		t.Fatalf("expected sub_lang=TC, got=%v", attrValues(res, model.AttrSubLang))
	}
	if !hasAttrValue(attrValues(res, model.AttrSubFlag), "hard") {
		t.Fatalf("expected sub_flag=hard, got=%v", attrValues(res, model.AttrSubFlag))
	}
	if hasAttrValue(notes, "CHN_YUE_AUDIO") || hasAttrValue(notes, "CHT_HARDSUB") {
		t.Fatalf("CHN_YUE_AUDIO/CHT_HARDSUB should be promoted, notes=%v", notes)
	}
}

func TestParserLexicon_RecruitmentPackAndRemasterVariantsAreConsumed(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Group][示例标题/Example Title][07][急招翻校轴][字幕組廣招新人][国日双语][附小剧场][重制版][12修正合集][1080p][WEB-DL]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	aliases := canonicalAliases(res)
	for _, a := range aliases {
		if strings.Contains(a, "急招翻校轴") ||
			strings.Contains(a, "字幕組廣招新人") ||
			strings.Contains(a, "国日双语") ||
			strings.Contains(a, "附小剧场") ||
			strings.Contains(a, "重制版") ||
			strings.Contains(a, "12修正合集") {
			t.Fatalf("meta token leaked into alias: %v", aliases)
		}
	}
	notes := attrValues(res, model.AttrNote)
	for _, want := range []string{
		"RECRUITMENT",
		"CHN_JPN_AUDIO",
		"BONUS_VIDEO",
		"REMASTERED_VERSION",
	} {
		if !hasAttrValue(notes, want) {
			t.Fatalf("expected note=%s, got=%v", want, notes)
		}
	}
	if !hasAttrValue(attrValues(res, model.AttrPackProfile), "curated_pack") {
		t.Fatalf("expected pack_profile=curated_pack, got=%v", attrValues(res, model.AttrPackProfile))
	}
	if hasAttrValue(notes, "CURATED_PACK") {
		t.Fatalf("CURATED_PACK should be promoted from note, notes=%v", notes)
	}
}

func TestParserEpisodeRange_TVPackSuffixSplit(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[1080P][DBD-Raws][极限女孩/UG*Ultimate Girls/UG☆アルティメットガール][01-12TV全集][HEVC-10bit][简繁外挂][FLAC][MKV]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalEpisodeRange(res); got != "1-12" {
		t.Fatalf("episode_range=%q want=1-12", got)
	}
	if hasAttrValue(attrValues(res, model.AttrNote), "TV_PACK") {
		t.Fatalf("TV_PACK note should be removed when pack_kind exists, notes=%v", attrValues(res, model.AttrNote))
	}
	if got := canonicalPackKind(res); got != "TV_PACK" {
		t.Fatalf("pack_kind=%q want=TV_PACK", got)
	}
	for _, tok := range res.Tokens {
		if strings.EqualFold(strings.TrimSpace(tok.Text), "12TV") && !tok.Removed {
			t.Fatalf("12TV should be consumed, tokens=%v", res.Tokens)
		}
	}
}

func TestParserEpisode_TailScheduleNoticeDoesNotOverridePrimary(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "【永恒译制】★1月新番★ Showtime！2～唱歌的大姐姐也想做～ / Showtime! 2 Uta no Onee-san Datte Shitai 03（Onair） GB 720P MP4【下周停播，2.6第4集】"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalEpisode(res); got != "3" {
		t.Fatalf("episode=%q want=3", got)
	}
}

func TestParserEpisodeRange_CJKSplitRangeWithSuffix(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "【极影字幕·毁片党】碧蓝之海2 Grand Blue Dreaming! S2 第01-12集 GB_CN HEVC_opus 1080p"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalEpisodeRange(res); got != "1-12" {
		t.Fatalf("episode_range=%q want=1-12", got)
	}
	if got := canonicalEpisode(res); got != "" {
		t.Fatalf("episode=%q want empty when range detected", got)
	}
}

func TestParserAliasCleanup_BracketTitleFallbackWhenMainTitleEmpty(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "【喵萌Production】★07月新番★[LoveLive! 超級明星!! 2期 / Love Live! Superstar!! S2][04][WebRip][1080p][繁日雙語][招募翻譯]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	title := canonicalTitle(res)
	aliases := canonicalAliases(res)

	if strings.TrimSpace(title) == "" {
		t.Fatalf("title should not be empty, aliases=%v", aliases)
	}
	if strings.Contains(title, "/") {
		t.Fatalf("title should not keep slash-merged aliases: %q", title)
	}
	if !containsAlias(aliases, "Love Live! Superstar!!") && !containsAlias(aliases, "Love Live! Superstar!! S2") {
		t.Fatalf("expected alias from bracket title split, got=%v", aliases)
	}
}

func TestParserGroupRouteOverride_OnePaceFixedTitle(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[One Pace][23-41] Syrup Village [1080p]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	group := canonicalGroup(res)
	title := canonicalTitle(res)
	aliases := canonicalAliases(res)
	episodeRange := canonicalEpisodeRange(res)
	if group != "One Pace" {
		t.Fatalf("group=%q want=%q", group, "One Pace")
	}
	if title != "One Piece" {
		t.Fatalf("title=%q want=%q", title, "One Piece")
	}
	if len(aliases) != 0 {
		t.Fatalf("aliases should be empty, got=%v", aliases)
	}
	if episodeRange != "23-41" {
		t.Fatalf("episode_range=%q want=%q", episodeRange, "23-41")
	}
	if res.TemplateID != "TF_BATCH_RANGE_ONEPACE_01" {
		t.Fatalf("template_id=%q want=%q", res.TemplateID, "TF_BATCH_RANGE_ONEPACE_01")
	}
}

func TestParserGroupRouteOverride_GroupEPRouteWGDL(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[晚街与灯][命运-奇异赝品_Fate strange Fake][09 噩梦与伦敦的黎明一同到来][WebRip][1080P_AVC_AAC][简日双语内嵌]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalEpisode(res); got != "9" {
		t.Fatalf("episode=%q want=%q", got, "9")
	}
	if got := canonicalGroup(res); got != "晚街与灯" {
		t.Fatalf("group=%q want=%q", got, "晚街与灯")
	}
	if got := canonicalTitle(res); got == "" {
		t.Fatalf("title should not be empty")
	}
	if got := canonicalTitle(res); strings.Contains(strings.ToLower(got), "fate") {
		t.Fatalf("title should keep zh main title only for this route, got=%q", got)
	}
	aliases := canonicalAliases(res)
	if !containsAlias(aliases, "Fate strange Fake") {
		t.Fatalf("expected alias Fate strange Fake, got=%v", aliases)
	}
	if got := canonicalEPSource(res); got != "group_route_override" {
		t.Fatalf("expected group_route_override source, got=%q", got)
	}
	for _, a := range aliases {
		if strings.Contains(a, "09 ") || strings.HasPrefix(strings.TrimSpace(a), "09_") {
			t.Fatalf("episode-like alias should be pruned, aliases=%v", aliases)
		}
	}
	if res.ContentKind == "MOVIE" {
		t.Fatalf("content_kind should not remain MOVIE, got=%q", res.ContentKind)
	}
}

func TestParserGroupRouteOverride_GroupEPRouteWGDLUnderscoreSeparatorApplied(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[晚街与灯][这里是充满笑容的职场。_Egao no Taenai Shokuba Desu][11_亲身体验是很重要的。][WEB-DL Remux][1080P_AVC_AAC][简繁日双语内封]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalEpisode(res); got != "11" {
		t.Fatalf("episode=%q want=%q", got, "11")
	}
	if got := canonicalEPSource(res); got != "group_route_override" {
		t.Fatalf("expected group_route_override source, got=%q", got)
	}
	if got := canonicalTitle(res); got == "" || strings.Contains(got, "_") {
		t.Fatalf("unexpected title=%q", got)
	}
	aliases := canonicalAliases(res)
	if !containsAlias(aliases, "Egao no Taenai Shokuba Desu") {
		t.Fatalf("expected alias Egao no Taenai Shokuba Desu, got=%v", aliases)
	}
}

func TestParserGroupRouteOverride_AtlasUnderscoreSeparatorApplied(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[阿特拉斯字幕组·雪原市出差所][命运-奇异赝品_Fate／strange Fake][04_半神们的卡农曲][简繁日内封PGS][日语配音版_Japanese Dub][Web-DL Remux][1080p AVC AAC]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalEpisode(res); got != "4" {
		t.Fatalf("episode=%q want=%q", got, "4")
	}
	if got := canonicalTitle(res); strings.Contains(strings.ToLower(got), "fate") {
		t.Fatalf("title should keep zh main title only for this route, got=%q", got)
	}
	aliases := canonicalAliases(res)
	if !containsAlias(aliases, "Fate/strange Fake") {
		t.Fatalf("expected alias Fate/strange Fake, got=%v", aliases)
	}
	if containsAlias(aliases, "strange Fake") {
		t.Fatalf("fragment alias should be pruned, got=%v", aliases)
	}
	if got := canonicalEPSource(res); got != "group_route_override" {
		t.Fatalf("expected group_route_override source, got=%q", got)
	}
}

func TestParserGroupRouteOverride_AtlasSpaceSeparatorNotApplied(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[阿特拉斯字幕组·雪原市出差所][命运-奇异赝品_Fate／strange Fake][04 半神们的卡农曲][简繁日内封PGS][日语配音版_Japanese Dub][Web-DL Remux][1080p AVC AAC]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalEPSource(res); got == "group_route_override" {
		t.Fatalf("space separator should not hit atlas group override, ep_semantics=%+v", canonicalEPSemantics(res))
	}
}

func TestParserGroupRouteOverride_GroupEPRouteAtlasWithSP(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[阿特拉斯字幕组][命运-奇异赝品 -黎明低语-_Fate／strange Fake -Whispers of Dawn-][00_SP][简繁英内封PGS][English Dub][Web-DL Remux][1080p HEVC AAC]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalEpisode(res); got != "0" {
		t.Fatalf("episode=%q want=%q", got, "0")
	}
	if got := canonicalSPKind(res); got != "SP" {
		t.Fatalf("sp_kind=%q want=%q", got, "SP")
	}
	if res.ContentKind == "MOVIE" {
		t.Fatalf("content_kind should not remain MOVIE, got=%q", res.ContentKind)
	}
	for _, a := range canonicalAliases(res) {
		if strings.EqualFold(strings.TrimSpace(a), "00_SP") {
			t.Fatalf("episode-like alias should be pruned, aliases=%v", canonicalAliases(res))
		}
	}
}

func TestParserUnknownTemplate_GTAliasMinimalAssigned(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	cases := []string{
		"[LoliHouse] 保龄球少女！ / Turkey!",
		"[Inka-Subs] Armor Hunter Merowlink (Mellowlink)",
	}
	for _, raw := range cases {
		res, err := p.Parse(raw)
		if err != nil {
			t.Fatalf("parse failed for %q: %v", raw, err)
		}
		if got := res.TemplateID; got != "TF_UNKNOWN_G_T_ALIAS_MINIMAL_01" {
			t.Fatalf("template_id=%q want=%q raw=%q", got, "TF_UNKNOWN_G_T_ALIAS_MINIMAL_01", raw)
		}
		if got := res.ContentKind; got != "UNKNOWN" {
			t.Fatalf("content_kind=%q want=UNKNOWN raw=%q", got, raw)
		}
		if strings.TrimSpace(canonicalGroup(res)) == "" || strings.TrimSpace(canonicalTitle(res)) == "" {
			t.Fatalf("group/title should be non-empty raw=%q attrs=%v", raw, res.Attributes)
		}
	}
}

func TestParserUnknownTemplate_TAliasMinimalAssigned(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "Mahou no Stage Fancy Lala  / Fancy Lala"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.TemplateID; got != "TF_UNKNOWN_T_ALIAS_MINIMAL_01" {
		t.Fatalf("template_id=%q want=%q", got, "TF_UNKNOWN_T_ALIAS_MINIMAL_01")
	}
	if got := res.ContentKind; got != "UNKNOWN" {
		t.Fatalf("content_kind=%q want=UNKNOWN", got)
	}
	if strings.TrimSpace(canonicalGroup(res)) != "" {
		t.Fatalf("group should be empty for no-group minimal route, got=%q", canonicalGroup(res))
	}
	if strings.TrimSpace(canonicalTitle(res)) == "" {
		t.Fatalf("title should be non-empty")
	}
}

func TestParserUnknownTemplate_TAliasMinimalNotAppliedWithoutAliasSeparator(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "City Hunter 1987 [Rus]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.TemplateID; got == "TF_UNKNOWN_T_ALIAS_MINIMAL_01" {
		t.Fatalf("T_ALIAS minimal should not apply without alias separator, raw=%q", raw)
	}
}

func TestParserUnknownTemplate_GTAliasMinimalNotAppliedWithTech(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[LoliHouse] Clevatess -魔兽之王与赤子与尸之勇者- / Clevatess: Majuu no Ou to Akago to Shikabane no Yuusha [1080p]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.TemplateID; got == "TF_UNKNOWN_G_T_ALIAS_MINIMAL_01" {
		t.Fatalf("minimal unknown template should not be used when tech exists, raw=%q", raw)
	}
}

func TestParserGroupRouteOverride_GroupEPRouteMarukoNotMovie(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[丸子家族][海螺小姐(Sazae-san)][2712]鲑仔和电线杆叔叔&不要责怪我把自己当女儿&喜欢恐怖电影吗[2023.08.06][GB][1080P][MP4]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalGroup(res); got != "丸子家族" {
		t.Fatalf("group=%q want=%q", got, "丸子家族")
	}
	if got := canonicalEpisode(res); got != "2712" {
		t.Fatalf("episode=%q want=%q", got, "2712")
	}
	if got := res.ContentKind; got != "TV_EP" {
		t.Fatalf("content_kind=%q want=%q", got, "TV_EP")
	}
	if notes := attrValues(res, model.AttrNote); !containsAlias(notes, "鲑仔和电线杆叔叔&不要责怪我把自己当女儿&喜欢恐怖电影吗") {
		t.Fatalf("missing maruko eptitle note, notes=%v", notes)
	}
	if notes := attrValues(res, model.AttrNote); !containsAlias(notes, "2023.08.06") {
		t.Fatalf("missing maruko airdate note, notes=%v", notes)
	}
	if sem := canonicalEPSemantics(res); sem == nil {
		t.Fatalf("ep_semantics should not be nil")
	}
}

func TestParserGroupRouteOverride_MarukoAirdateDoesNotBecomeEpisode(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[丸子家族][樱桃小丸子(Chibi Maruko-chan)]樱花盛开 祝大家幸福！春季1小时特别篇[2026.03.29][GB][1080P][MP4]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalGroup(res); got != "丸子家族" {
		t.Fatalf("group=%q want=%q", got, "丸子家族")
	}
	if got := canonicalEpisode(res); got != "" {
		t.Fatalf("episode=%q want empty", got)
	}
	if got := res.ContentKind; got == "TV_EP" && canonicalEPSource(res) == "group_route_override" {
		t.Fatalf("date-only third bracket should not trigger maruko episode override, ep_semantics=%+v", canonicalEPSemantics(res))
	}
	if aliases := canonicalAliases(res); containsAlias(aliases, "2026.03 29") || containsAlias(aliases, "2026.03.29") {
		t.Fatalf("date bracket should not become title alias, aliases=%v", aliases)
	}
	if got := canonicalTitle(res); !strings.Contains(got, "特别篇") && !strings.Contains(got, "特別篇") {
		t.Fatalf("title=%q should keep special title text", got)
	}
}

func TestParserGroupRouteOverride_FurretarDubPrefixCleanup(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Furretar] 台配 - 入间同学入魔了！ (Mairimashita! Iruma-kun) (Season 1) (Mandarin Chinese Dub) (Matching Soft Subs) [國語][国语][中文配音][中配版] (入間同學入魔了！)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	title := canonicalTitle(res)
	if strings.HasPrefix(title, "台配") || strings.HasPrefix(title, "中台配") {
		t.Fatalf("furretar title prefix should be cleaned, got=%q", title)
	}
	if title != "入间同学入魔了！" {
		t.Fatalf("title=%q want=%q", title, "入间同学入魔了！")
	}
	if !hasAttrValue(attrValues(res, model.AttrAudioLang), "CMN") {
		t.Fatalf("expected audio_lang CMN, got=%v", attrValues(res, model.AttrAudioLang))
	}
}

func TestParserGroupRouteOverride_FurretarCleanupNoGlobalDrift(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[TestGroup] 台配 - 入间同学入魔了！ (Season 1)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	title := canonicalTitle(res)
	if !strings.HasPrefix(title, "台配") {
		t.Fatalf("non-furretar title should keep prefix, got=%q", title)
	}
}

func TestParserGroupRouteOverride_AniSpecialNoEpisodeForcesSpecial(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[ANi] 無職轉生～到了異世界就拿出真本事 第二季 - 特別篇 [1080P][Baha][WEB-DL][AAC AVC][CHT][V2][MP4]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "SPECIAL" {
		t.Fatalf("content_kind=%q want=%q", got, "SPECIAL")
	}
	if got := res.TemplateID; got != "TF_TV_SP_ONLY_01" {
		t.Fatalf("template_id=%q want=%q", got, "TF_TV_SP_ONLY_01")
	}
	if got := canonicalEpisode(res); got != "" {
		t.Fatalf("episode=%q want empty", got)
	}
	if got := canonicalSPKind(res); got != "SP" {
		t.Fatalf("sp_kind=%q want=%q", got, "SP")
	}
}

func TestParserGroupRouteOverride_AniSpecialWithEpisodeStaysTVEP(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[ANi] 关于我转生变成史莱姆这档事 第三季 [特别篇] - 01 [1080P][Baha][WEB-DL][AAC AVC][CHT][MP4]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "TV_EP" {
		t.Fatalf("content_kind=%q want=%q", got, "TV_EP")
	}
	if got := canonicalEpisode(res); got != "1" {
		t.Fatalf("episode=%q want=%q", got, "1")
	}
}

func TestParserGroupRouteOverride_UnderscoreDelimiterRouteFS(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[FS]_Azur_Lane_-_01~12_[BD_1920X1080_x264_AAC][Sub_Esp][MP4]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalTitle(res); got != "Azur Lane" {
		t.Fatalf("title=%q want=%q", got, "Azur Lane")
	}
	if strings.Contains(canonicalTitle(res), "_") {
		t.Fatalf("underscore should be consumed in title, got=%q", canonicalTitle(res))
	}
	if got := canonicalEpisodeRange(res); got != "1-12" {
		t.Fatalf("episode_range=%q want=%q", got, "1-12")
	}
}

func TestParserGroupRouteOverride_UnderscoreDelimiterRouteKaerizaki(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Kaerizaki-Fansub]_One_Piece_1144_[VERSION_LIGHT][VOSTFR][FHD_1920x1080].mp4"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalTitle(res); got != "One Piece 1144" {
		t.Fatalf("title=%q want=%q", got, "One Piece 1144")
	}
	if strings.Contains(canonicalTitle(res), "_") {
		t.Fatalf("underscore should be consumed in title, got=%q", canonicalTitle(res))
	}
}

func TestParserTitleAlias_BracketUnderscoreBridgeCJKLatin(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[悠哈璃羽字幕社][RPG不动产_RPG Fudousan][01][x264 1080p][CHS]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalTitle(res); got != "RPG不动产" {
		t.Fatalf("title=%q want=%q", got, "RPG不动产")
	}
	aliases := canonicalAliases(res)
	if !containsAlias(aliases, "RPG Fudousan") {
		t.Fatalf("expected alias RPG Fudousan, got=%v", aliases)
	}
}

func TestParserTitleAlias_BracketUnderscoreBridge_NoSplitForLatinOnly(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Test][One_Piece][01][1080p]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalTitle(res); got != "One Piece" {
		t.Fatalf("title=%q want=%q", got, "One Piece")
	}
	if aliases := canonicalAliases(res); len(aliases) != 0 {
		t.Fatalf("latin-only underscore should not create alias, got=%v", aliases)
	}
}

func TestParserAliasCleanup_FurretarDropsMovieMetaAlias(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	cases := []struct {
		name     string
		raw      string
		notAlias []string
		contents []string
		packKind string
		profile  string
	}{
		{
			name: "[Movie] should not leak",
			raw:  "[Furretar] 台配 - 阿基拉 (AKIRA) (Movie) (Mandarin Chinese Dub) (Matching Soft Subs) [國語][国语][中文配音][中配版]",
			notAlias: []string{
				"Movie",
			},
			contents: []string{"MOVIE"},
			profile:  "movie_only",
		},
		{
			name: "(Movies + Specials) should not leak",
			raw:  "[Furretar] 台配 - 名侦探柯南 (Detective Conan) (Movies + Specials) (Mandarin Chinese Dub) (Matching Soft Subs) [國語][国语][中文配音][中配版] (Case Closed) (Meitantei Conan)",
			notAlias: []string{
				"Movies + Specials",
			},
			contents: []string{"MOVIE", "SPECIAL"},
			packKind: "contents",
			profile:  "movie_special_mix",
		},
		{
			name: "(Season x + Movie) fragment should not leak",
			raw:  "[Furretar] 台配 - 关于我转生变成史莱姆这档事 (Tensei Shitara Slime Datta Ken) (Season 1-3 + Movie) (Mandarin Chinese Dub) (Matching Soft Subs) [國語][国语][中文配音][中配版] (關於我轉生變成史萊姆這檔事)",
			notAlias: []string{
				"3 + Movie",
				"Season 1-3 + Movie",
			},
			contents: []string{"TV", "MOVIE"},
			profile:  "movie_series_mix",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := p.Parse(tc.raw)
			if err != nil {
				t.Fatalf("parse failed: %v", err)
			}
			aliases := canonicalAliases(res)
			for _, ban := range tc.notAlias {
				if containsAlias(aliases, ban) {
					t.Fatalf("forbidden alias leaked: %q aliases=%v", ban, aliases)
				}
			}
			gotContents := attrValues(res, model.AttrContents)
			for _, want := range tc.contents {
				if !hasAttrValue(gotContents, want) {
					t.Fatalf("missing contents marker %q, got=%v", want, gotContents)
				}
			}
			if tc.packKind != "" {
				if got := canonicalPackKind(res); !strings.EqualFold(got, tc.packKind) {
					t.Fatalf("pack_kind=%q want=%q", got, tc.packKind)
				}
			}
			if tc.profile != "" {
				if got := canonicalPackProfile(res); !strings.EqualFold(got, tc.profile) {
					t.Fatalf("pack_profile=%q want=%q", got, tc.profile)
				}
			}
		})
	}
}

func TestParserContentsMarker_CJKMovieMarkerInBracket(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[TestGroup] 示例标题 [剧场版][1080p][WEB-DL]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !hasAttrValue(attrValues(res, model.AttrContents), "MOVIE") {
		t.Fatalf("expected contents MOVIE, got=%v", attrValues(res, model.AttrContents))
	}
}

func TestParserContentsMarker_EnglishMovieBracketPromotedFromNote(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[TestGroup] Sample Title [Movie] [1080p][WEB-DL]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !hasAttrValue(attrValues(res, model.AttrContents), "MOVIE") {
		t.Fatalf("expected contents MOVIE, got=%v", attrValues(res, model.AttrContents))
	}
	if hasAttrValue(attrValues(res, model.AttrNote), "MOVIE") {
		t.Fatalf("MOVIE note should be promoted to contents, notes=%v", attrValues(res, model.AttrNote))
	}
}

func TestParserContentsMarker_CJKMainPlusOVAInBracket(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[TestGroup] 示例标题 [正片+OVA][1080p][BDRip]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	got := attrValues(res, model.AttrContents)
	if !hasAttrValue(got, "MOVIE") {
		t.Fatalf("expected contents MOVIE, got=%v", got)
	}
	if !hasAttrValue(got, "OVA") {
		t.Fatalf("expected contents OVA, got=%v", got)
	}
}

func TestParserContentsMarker_CJKMovieWordInsideTitleSpanNotContents(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[4K_SDR][DBD-Raws][名侦探柯南 剧场版 06 贝克街的亡灵/Detective Conan: The Phantom of Baker Street][2160P][BDRip][HEVC-10bit][简体外挂][FLAC][MKV]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	contents := attrValues(res, model.AttrContents)
	if hasAttrValue(contents, "MOVIE") {
		t.Fatalf("in-title 剧场版 should not emit contents MOVIE, got=%v", contents)
	}
}

func TestParserEPSlotSurfaceTokenConsumed_ChineseEpisodeWord(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Test] 示例标题 第12话 [1080p][WEB-DL]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalEpisode(res); got != "12" {
		t.Fatalf("episode=%q want=12", got)
	}
	for _, tok := range res.Tokens {
		if strings.EqualFold(strings.TrimSpace(tok.Text), "第12话") && !tok.Removed {
			t.Fatalf("epslot surface token should be consumed, tokens=%v", res.Tokens)
		}
	}
}

func TestParserEPSlotSurfaceTokenConsumed_EPToken(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "Biao Ren Blades of the Guardians EP01 vostfr."
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalEpisode(res); got != "1" {
		t.Fatalf("episode=%q want=1", got)
	}
	for _, tok := range res.Tokens {
		if strings.EqualFold(strings.TrimSpace(tok.Text), "EP01") && !tok.Removed {
			t.Fatalf("epslot surface token should be consumed, tokens=%v", res.Tokens)
		}
	}
}

func TestParserAliasCleanup_FurretarDropsLangMetaAliases(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Furretar] 台配 - 进击的巨人 The Final Season 完结篇 [繁字][CHT][國語][中文配音]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	aliases := canonicalAliases(res)
	for _, ban := range []string{"繁字", "國語", "国语", "中文配音", "中配版", "音"} {
		if containsAlias(aliases, ban) {
			t.Fatalf("forbidden language/meta alias leaked: %q aliases=%v", ban, aliases)
		}
	}
}

func TestParserAliasCleanup_DropsPlatformTagAliasDSNP(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "Cat's Eye (2025) S01E01 MULTi 1080p WEB H.264 AAC -Tsundere-Raws (DSNP) (VF, FRENCH, VOSTFR, Cat's♥Eye)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	aliases := canonicalAliases(res)
	if containsAlias(aliases, "DSNP") {
		t.Fatalf("platform tag should not be alias, aliases=%v", aliases)
	}
}

func TestParserAliasCleanup_DropsPartAliasFragment(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[酷漫404][进击的巨人 最终季 Part.2][76][1080P][WebRip][简日对白字幕+特效注解字幕][HEVC-10bit AAC][MKV][字幕组招人內详]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	aliases := canonicalAliases(res)
	if containsAlias(aliases, "Part") {
		t.Fatalf("part fragment should not be alias, aliases=%v", aliases)
	}
	title := canonicalTitle(res)
	if !strings.Contains(strings.ToLower(title), "part 2") {
		t.Fatalf("part suffix should remain in primary title when sourced from titleslot, got=%q", title)
	}
}

func TestParserStableTechSpanCombo_CommaSeparatedCodecBlock(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[CicakRumah] 2200-nen Neko no Kuni Nippon - S01E09 (2200年ねこの国ニッポン) [ABEMA.WEB-DL 1080P AVC, AAC, DUAL][001EC424]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	foundVideo := false
	foundAudio := false
	foundDual := false
	for _, a := range res.Attributes {
		if a.Type == model.AttrVideoCodec && strings.EqualFold(strings.TrimSpace(a.Normalized), "AVC") {
			foundVideo = true
			if a.Source == "template_float_infer" {
				t.Fatalf("video_codec should be resolved before template float infer, got source=%q", a.Source)
			}
		}
		if a.Type == model.AttrAudioCodec && strings.EqualFold(strings.TrimSpace(a.Normalized), "AAC") {
			foundAudio = true
			if a.Source == "template_float_infer" {
				t.Fatalf("audio_codec should be resolved before template float infer, got source=%q", a.Source)
			}
		}
		if a.Type == model.AttrAudioFlag && strings.EqualFold(strings.TrimSpace(a.Normalized), "DUAL_AUDIO") {
			foundDual = true
		}
	}
	if !foundVideo || !foundAudio {
		t.Fatalf("expected AVC/AAC from stable tech span combo, got attrs=%v", res.Attributes)
	}
	if !foundDual {
		t.Fatalf("expected DUAL_AUDIO from stable tech span combo, got attrs=%v", res.Attributes)
	}
	if !hasAttrValue(attrValues(res, model.AttrCRC), "001EC424") {
		t.Fatalf("expected crc=001EC424, got=%v", attrValues(res, model.AttrCRC))
	}
	for _, tok := range res.Tokens {
		if strings.EqualFold(strings.TrimSpace(tok.Text), "001EC424") && !tok.Removed {
			t.Fatalf("crc token should be consumed, tokens=%v", res.Tokens)
		}
	}
}

func TestParserStableTechSpanCombo_GluedCodecBitDepth(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[诸神字幕组][NHK][古罗马街道 穿越时空的旅行 阿庇亚大道 贯穿意大利580公里][1080P HEVC10 MKV]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !hasAttrValue(attrValues(res, model.AttrVideoCodec), "HEVC") {
		t.Fatalf("expected video_codec=HEVC, got=%v", attrValues(res, model.AttrVideoCodec))
	}
	if !hasAttrValue(attrValues(res, model.AttrBitDepth), "10bit") {
		t.Fatalf("expected bit_depth=10bit, got=%v", attrValues(res, model.AttrBitDepth))
	}
	for _, tok := range res.Tokens {
		if strings.EqualFold(strings.TrimSpace(tok.Text), "HEVC10") && !tok.Removed {
			t.Fatalf("HEVC10 should be consumed, tokens=%v", res.Tokens)
		}
	}
}

func TestParserStableTechSpanCombo_GluedCodecBitDepthWithP60(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[诸神字幕组][NHK纪录片][历史秘话之EVA][2020-04-14][1080P60 HEVC10 MKV]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !hasAttrValue(attrValues(res, model.AttrVideoCodec), "HEVC") {
		t.Fatalf("expected video_codec=HEVC, got=%v", attrValues(res, model.AttrVideoCodec))
	}
	if !hasAttrValue(attrValues(res, model.AttrBitDepth), "10bit") {
		t.Fatalf("expected bit_depth=10bit, got=%v", attrValues(res, model.AttrBitDepth))
	}
	for _, tok := range res.Tokens {
		if strings.EqualFold(strings.TrimSpace(tok.Text), "HEVC10") && !tok.Removed {
			t.Fatalf("HEVC10 should be consumed, tokens=%v", res.Tokens)
		}
	}
}

func TestParserEpisodePriority_BracketTitleLeadingNumberSweetSub(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[SweetSub][16 位元的輝煌 Another Layer][16bit Sensation: Another Layer][01][WebRip][1080P][AVC 8bit][繁日雙語][v2]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	episode := canonicalEpisode(res)
	title := canonicalTitle(res)
	if episode != "1" {
		t.Fatalf("episode=%q want=%q", episode, "1")
	}
	if strings.TrimSpace(title) == "" {
		t.Fatalf("title should not be empty")
	}
}

func TestParserEpisodePriority_BracketTitleLeadingNumberCCAnime(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[c.c動漫][10月新番][86-不存在的戰區][12][BIG5][1080P][MP4]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	episode := canonicalEpisode(res)
	title := canonicalTitle(res)
	if episode != "12" {
		t.Fatalf("episode=%q want=%q", episode, "12")
	}
	if !strings.Contains(title, "不存在的戰區") {
		t.Fatalf("title=%q should contain 86 title body", title)
	}
}

func TestParserEPSlotPrimary_SxxExxHasPrimary(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "9nine- Ruler's Crown S01E11 VOSTFR 1080p WEB h264 AAC - Yangire-Raws (YUZUKITV)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalEpisode(res); got != "11" {
		t.Fatalf("episode=%q want=%q", got, "11")
	}
	if sem := canonicalEPSemantics(res); sem == nil {
		t.Fatalf("ep_semantics should not be nil for sxxexx title")
	}
	if got := canonicalEPSource(res); got != "sxxexx_final" {
		t.Fatalf("ep_semantics.source=%q want=%q", got, "sxxexx_final")
	}
	if got := canonicalSeason(res); got != "1" {
		t.Fatalf("season=%q want=%q", got, "1")
	}
}

func TestParserEPSlotPrimary_SxxExxE00AsEpisodeZero(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "Towa.No.Yuugure.S01E00.VOSTFR.1080p.WEBRiP.AV1-KAF"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalEpisode(res); got != "0" {
		t.Fatalf("episode=%q want=%q", got, "0")
	}
	if sem := canonicalEPSemantics(res); sem == nil {
		t.Fatalf("ep_semantics should not be nil for sxxexx e00 title")
	}
	if got := canonicalEPSource(res); got != "sxxexx_final" {
		t.Fatalf("ep_semantics.source=%q want=%q", got, "sxxexx_final")
	}
}

func TestParserEpisodeDashBoundary_BeybladeStyle(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "Beyblade X - 70 - Dread Dragon [English Dub][1080p][MW]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalEpisode(res); got != "70" {
		t.Fatalf("episode=%q want=%q", got, "70")
	}
}

func TestParserEpisodeDashBoundary_AllowsVersionAndEndFlag(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	cases := []struct {
		name string
		raw  string
		ep   string
		ver  string
	}{
		{
			name: "dash_ep_with_version",
			raw:  "[TestGroup] Sample Show - 01 v2 [1080p][WEB-DL]",
			ep:   "1",
			ver:  "v2",
		},
		{
			name: "dash_ep_with_endflag",
			raw:  "[TestGroup] Sample Show - 12 END [1080p][WEB-DL]",
			ep:   "12",
			ver:  "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := p.Parse(tc.raw)
			if err != nil {
				t.Fatalf("parse failed: %v", err)
			}
			if got := canonicalEpisode(res); got != tc.ep {
				t.Fatalf("episode=%q want=%q", got, tc.ep)
			}
			if got := canonicalVersion(res); got != tc.ver {
				t.Fatalf("version=%q want=%q", got, tc.ver)
			}
		})
	}
}

func TestParserEpisodePriority_StrongDashBeatsEpisodeKeyword(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Fansub] Sample Show Episode 16 Preview - 09 [1080p][WEB]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalEpisode(res); got != "9" {
		t.Fatalf("episode=%q want=%q", got, "9")
	}
}

func TestParserEpisodePriority_KeywordEpisodeStillWorks(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Fansub] Sample Show Episode 52 [1080p][WEB]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalEpisode(res); got != "52" {
		t.Fatalf("episode=%q want=%q", got, "52")
	}
}

func TestParserEpisodePriority_DashBitDepthTokenNotEpisode(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Fansub] Sample Show - 24 Bit Remaster - 01 [1080p][WEB]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalEpisode(res); got != "1" {
		t.Fatalf("episode=%q want=%q", got, "1")
	}
}

func TestParserEpisodeZero_DashZeroStyle(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[ANi] KamitsubakiShi Kensetsuchū / 神椿市建設中。 - 00 [1080P][Baha][WEB-DL][AAC AVC][CHT][MP4]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalEpisode(res); got != "0" {
		t.Fatalf("episode=%q want=%q", got, "0")
	}
}

func TestParserEpisodeRange_EpisodeKeywordRange(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Furretar] 台配 - BLEACH 千年血战篇 -相克谭- (Season 17 Episode 27-40)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalEpisodeRange(res); got != "27-40" {
		t.Fatalf("episode_range=%q want=%q", got, "27-40")
	}
}

func TestParserEpisodeChineseLeadingVariants(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	cases := []struct {
		name string
		raw  string
		ep   string
	}{
		{
			name: "bracket_ch_ep_with_marker",
			raw:  "[AngelEcho]辺境都市の育成者 边境都市的培养者 [第01话②]",
			ep:   "1",
		},
		{
			name: "ch_ep_with_tail_text",
			raw:  "[AngelEcho]とじのみこ四コマ計画 刀使之巫女四格计划 第14话&最终话",
			ep:   "14",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := p.Parse(tc.raw)
			if err != nil {
				t.Fatalf("parse failed: %v", err)
			}
			if got := canonicalEpisode(res); got != tc.ep {
				t.Fatalf("episode=%q want=%q", got, tc.ep)
			}
		})
	}
}

func TestParserNoiseWrapper_UnwrapTransportNestedBracket(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[搬運[ANi] You and I Are Polar Opposites / 相反的你和我 - 01 [1080P][Baha][WEB-DL][AAC AVC][CHT][MP4]]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	title := canonicalTitle(res)
	group := canonicalGroup(res)
	if strings.TrimSpace(title) == "" {
		t.Fatalf("title should not be empty")
	}
	if !strings.EqualFold(strings.TrimSpace(group), "ANi") {
		t.Fatalf("group=%q want=%q", group, "ANi")
	}
}

func TestParserNoiseWrapper_SkipsLeadingTransportNoiseBracket(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	cases := []struct {
		name string
		raw  string
	}{
		{
			name: "traditional",
			raw:  "[搬運][ANi] 轉生之後的我變成了龍蛋～目標是世界最強～ - 01 [1080P][Baha][WEB-DL][AAC AVC][CHT][MP4]",
		},
		{
			name: "simplified",
			raw:  "[搬运][ANi] 轉生之後的我變成了龍蛋～目標是世界最強～ - 01 [1080P][Baha][WEB-DL][AAC AVC][CHT][MP4]",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := p.Parse(tc.raw)
			if err != nil {
				t.Fatalf("parse failed: %v", err)
			}
			if got := canonicalGroup(res); !strings.EqualFold(strings.TrimSpace(got), "ANi") {
				t.Fatalf("group=%q want=%q", got, "ANi")
			}
			if got := canonicalTitle(res); strings.Contains(got, "搬運") || strings.Contains(got, "搬运") {
				t.Fatalf("title leaked leading transport noise: %q", got)
			}
			if got := canonicalEpisode(res); got != "1" {
				t.Fatalf("episode=%q want=%q", got, "1")
			}
		})
	}
}

func TestParserNormalizeRawTitle_StripsZeroWidthPrefix(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "\u200b[c.c動漫][10月新番][境界觸發者 第三季][01][BIG5][1080P][MP4]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if strings.TrimSpace(canonicalTitle(res)) == "" {
		t.Fatalf("title should not be empty")
	}
	if strings.TrimSpace(canonicalGroup(res)) == "" {
		t.Fatalf("group should not be empty")
	}
}

func TestParserSingleVersionToken_UnknownCoreOnly(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[v1]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.ContentKind != "UNKNOWN" {
		t.Fatalf("content_kind=%q want UNKNOWN", res.ContentKind)
	}
	for _, a := range res.Attributes {
		if a.Type != model.AttrGroup && a.Type != model.AttrTitle && a.Type != model.AttrTitleAlias {
			t.Fatalf("unknown output leaked non-core attr: %+v", a)
		}
	}
}

func TestTemplateSignalTechStrict_InferPostEPCoreTechFromFloat(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Fansub] Example Show - 09 VOSTFR 720p WEB x.265 AAC -Tsundere-Raws (CR).mkv"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if canonicalEpisode(res) != "9" {
		t.Fatalf("episode=%q want=9", canonicalEpisode(res))
	}
	vcodecs := attrValues(res, model.AttrVideoCodec)
	if len(vcodecs) == 0 {
		t.Fatalf("expected video codec inferred from post-ep float token, attrs=%v", res.Attributes)
	}
	found := false
	for _, v := range vcodecs {
		low := strings.ToLower(v)
		if strings.Contains(low, "x265") || strings.Contains(low, "hevc") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected x265/hevc codec inferred from post-ep float token, got=%v", vcodecs)
	}
}

func TestTemplateSignalTechStrict_DoesNotInferPreTitleTechAsCodec(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Fansub] x.265 Example Show - 09 VOSTFR 720p WEB AAC -Tsundere-Raws (CR).mkv"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if canonicalEpisode(res) != "9" {
		t.Fatalf("episode=%q want=9", canonicalEpisode(res))
	}
	vcodecs := attrValues(res, model.AttrVideoCodec)
	for _, v := range vcodecs {
		if strings.Contains(strings.ToLower(v), "x265") {
			t.Fatalf("pre-title x.265 should not be inferred as video codec, got=%v", vcodecs)
		}
	}
}

func TestLexiconDict_ConfigFirst_WEB1080PAndCNSubFlags(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[TestGroup] Sample Show - 01 [WEB1080P][简繁内封][AAC][MKV]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if !hasAttrValue(attrValues(res, model.AttrSourceType), "WEBRip") {
		t.Fatalf("expected source_type WEBRip, got=%v", attrValues(res, model.AttrSourceType))
	}
	if !hasAttrValue(attrValues(res, model.AttrResolution), "1080p") {
		t.Fatalf("expected resolution 1080p, got=%v", attrValues(res, model.AttrResolution))
	}
	if !hasAttrValue(attrValues(res, model.AttrSubFlag), "soft") {
		t.Fatalf("expected sub_flag soft, got=%v", attrValues(res, model.AttrSubFlag))
	}
	if !hasAttrValue(attrValues(res, model.AttrSubLang), "SC") || !hasAttrValue(attrValues(res, model.AttrSubLang), "TC") {
		t.Fatalf("expected sub_lang SC/TC, got=%v", attrValues(res, model.AttrSubLang))
	}
}

func TestLexiconDict_ConfigFirst_VCDRipAndYueSubs(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[TestGroup] Sample Show - 01 [VCDRip][粤语中字][MP4]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if !hasAttrValue(attrValues(res, model.AttrSourceType), "VCDRip") {
		t.Fatalf("expected source_type VCDRip, got=%v", attrValues(res, model.AttrSourceType))
	}
	if !hasAttrValue(attrValues(res, model.AttrAudioLang), "YUE") {
		t.Fatalf("expected audio_lang YUE, got=%v", attrValues(res, model.AttrAudioLang))
	}
	if !hasAttrValue(attrValues(res, model.AttrSubLang), "ZH") {
		t.Fatalf("expected sub_lang ZH for 粤语中字, got=%v", attrValues(res, model.AttrSubLang))
	}
	if flags := attrValues(res, model.AttrSubFlag); len(flags) != 0 {
		t.Fatalf("expected no sub_flag for 粤语中字 fallback, got=%v", flags)
	}
}

func TestLexiconDict_ConfigFirst_GuoyuZhongzi_ToCMNAndZH(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[TestGroup] Sample Show - 01 [国语中字][MP4]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !hasAttrValue(attrValues(res, model.AttrAudioLang), "CMN") {
		t.Fatalf("expected audio_lang CMN for 国语, got=%v", attrValues(res, model.AttrAudioLang))
	}
	if !hasAttrValue(attrValues(res, model.AttrSubLang), "ZH") {
		t.Fatalf("expected sub_lang ZH for 中字, got=%v", attrValues(res, model.AttrSubLang))
	}
}

func TestLexiconDict_ConfigFirst_StandaloneYueAudioLang(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[TestGroup] Sample Show - 01 [粤语][MP4]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !hasAttrValue(attrValues(res, model.AttrAudioLang), "YUE") {
		t.Fatalf("expected audio_lang YUE, got=%v", attrValues(res, model.AttrAudioLang))
	}
}

func TestLexiconDict_ConfigFirst_YuePhraseVariants(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	cases := []struct {
		name string
		raw  string
	}{
		{name: "yue_dub_cn", raw: "[TestGroup] Sample Show - 01 [粤语配音][MP4]"},
		{name: "yue_no_sub_cn", raw: "[TestGroup] Sample Show - 01 [粤语无字][MP4]"},
		{name: "yue_version_tw", raw: "[TestGroup] Sample Show - 01 [粵語版][MP4]"},
		{name: "cantonese_dub_en", raw: "[TestGroup] Sample Show - 01 [CantoneseDub][WEB-DL][1080p]"},
		{name: "yue_eng_dual_cn", raw: "[TestGroup] Sample Show - 01 [粤英双语中字][MP4]"},
		{name: "yue_tag_tvb_cn", raw: "[TestGroup] Sample Show - 01 [TVB+發行商粵語][MP4]"},
		{name: "yue_reissue_cn", raw: "[TestGroup] Sample Show - 01 [代理商再版粵語][MP4]"},
		{name: "yue_agency_cn", raw: "[TestGroup] Sample Show - 01 [代理商粵語][MP4]"},
		{name: "yue_audio_track_cn", raw: "[TestGroup] Sample Show - 01 [日语_国语_粤语音轨][BDRip][1080P][MKV]"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := p.Parse(tc.raw)
			if err != nil {
				t.Fatalf("parse failed: %v", err)
			}
			if !hasAttrValue(attrValues(res, model.AttrAudioLang), "YUE") {
				t.Fatalf("expected audio_lang YUE, got=%v", attrValues(res, model.AttrAudioLang))
			}
		})
	}
}

func TestTagCodeAudioLang_CAUsesYUE(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Erai-raws] Sample Show - 01 (CA) [WEB-DL][1080p]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !hasAttrValue(attrValues(res, model.AttrAudioLang), "YUE") {
		t.Fatalf("expected tag code CA => audio_lang YUE, got=%v", attrValues(res, model.AttrAudioLang))
	}
}

func TestGroupSlot2PlatformInfer_JibaketaTVBYue(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[jibaketa合成&音頻壓制][TVB粵語] Sample Show - 01 [WEB-DL][1080p]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !hasAttrValue(attrValues(res, model.AttrPlatform), "TVB") {
		t.Fatalf("expected platform TVB, got=%v", attrValues(res, model.AttrPlatform))
	}
	if !hasAttrValue(attrValues(res, model.AttrAudioLang), "YUE") {
		t.Fatalf("expected audio_lang YUE, got=%v", attrValues(res, model.AttrAudioLang))
	}
}

func TestGroupRouteOverride_JibaketaStripsSlot2PrefixFromTitle(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[jibaketa合成&音頻壓制][TVB粵語]閃躍吧！星夢☆頻道 / 美妙☆频道 / Kiratto Pri-chan - 01 [粵日雙語+內封繁體中文字幕][WEB 1920x1080 x264 AACx2 SRT TVB CHT]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if !hasAttrValue(attrValues(res, model.AttrPlatform), "TVB") {
		t.Fatalf("expected platform TVB, got=%v", attrValues(res, model.AttrPlatform))
	}
	if !hasAttrValue(attrValues(res, model.AttrAudioLang), "YUE") {
		t.Fatalf("expected audio_lang YUE, got=%v", attrValues(res, model.AttrAudioLang))
	}
	title := canonicalTitle(res)
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(title)), "TVB") {
		t.Fatalf("title should strip TVB slot2 prefix, got=%q", title)
	}
	if !strings.Contains(title, "閃躍吧！星夢") {
		t.Fatalf("title core should be kept, got=%q", title)
	}
	if !containsAlias(canonicalAliases(res), "Kiratto Pri-chan") {
		t.Fatalf("expected alias Kiratto Pri-chan, got=%v", canonicalAliases(res))
	}
	for _, tok := range res.Tokens {
		txt := strings.TrimSpace(tok.Text)
		if (txt == "/" || strings.EqualFold(txt, "Kiratto") || strings.EqualFold(txt, "Pri") || strings.EqualFold(txt, "美妙")) && !tok.Removed {
			t.Fatalf("alias-chain residual token should be consumed, token=%q", txt)
		}
	}
}

func TestConsumeAliasChainTokensByFinalAliases_Jibaketa(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)
	raw := "[jibaketa合成&音頻壓制][TVB粵語]閃躍吧！星夢☆頻道 / 美妙☆频道 / Kiratto Pri-chan - 01 [粵日雙語+內封繁體中文字幕][WEB 1920x1080 x264 AACx2 SRT TVB CHT]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	cp := make([]model.Token, len(res.Tokens))
	copy(cp, res.Tokens)
	epIdx := episodeTokenStart(res)
	seasonIdx := -1
	epRangeIdx := -1
	p.consumeAliasChainTokensByFinalAliases(cp, [2]int{0, 4}, canonicalTitle(res), canonicalAliases(res), epIdx, epRangeIdx, seasonIdx, res.Attributes)
	removedSlash := 0
	for _, tok := range cp {
		if strings.TrimSpace(tok.Text) == "/" && tok.Removed {
			removedSlash++
		}
	}
	if removedSlash == 0 {
		t.Fatalf("expected slash tokens consumed by post cleanup")
	}
}

func TestConsumeAliasChainTokensByFinalAliases_JibaketaBilingualSlashOnly(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)
	raw := "[jibaketa合成&二次壓制][TVB粵語]MIX 第二季~第二個夏天，邁向晴空~ / Mix - Meisei Story 2nd Season - 01 [粵語+內封繁體中文字幕] [BD 1920x1080 x264 AAC SRT TVB YUE]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	epIdx := episodeTokenStart(res)
	if epIdx < 0 {
		t.Fatalf("expected episode token index")
	}
	for i := 0; i < epIdx && i < len(res.Tokens); i++ {
		if strings.TrimSpace(res.Tokens[i].Text) != "/" {
			continue
		}
		if !res.Tokens[i].Removed {
			t.Fatalf("expected pre-episode bilingual slash consumed, idx=%d", i)
		}
	}
}

func TestGroupSpecificTitleChainDelimiterCleanup_JibaketaViuTVSlash(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)
	raw := "[jibaketa合成&壓制][ViuTV粵語]约定的夢幻島 S2 / Yakusoku no Neverland 2nd Season - 01 [粵日雙語+內封繁體中文雙字幕] (BD 1920x1080 x264 AACx2 PGS+SRT ViuTV CHT)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	epIdx := episodeTokenStart(res)
	if epIdx < 0 {
		t.Fatalf("expected episode token index")
	}
	for i := 0; i < epIdx && i < len(res.Tokens); i++ {
		if strings.TrimSpace(res.Tokens[i].Text) != "/" {
			continue
		}
		if !res.Tokens[i].Removed {
			t.Fatalf("expected jibaketa bilingual title-chain slash consumed, idx=%d", i)
		}
	}
}

func TestGroupExtract_JibaketaPlusCompoundGroup(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)
	raw := "[jibaketa合成&音頻壓制+部份二次壓制][ViuTV粵語]捉捉！迷你寶 / 奇妙！萌可 第1季 / Catch! Teenieping [粵語+無字幕] (WEB 1920x1080 x264 AAC YUE)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	title := canonicalTitle(res)
	if strings.Contains(title, "jibaketa") || strings.Contains(title, "合成") {
		t.Fatalf("title should not collapse to group payload, got=%q", title)
	}
	groupVals := attrValues(res, model.AttrGroup)
	if len(groupVals) == 0 || !strings.Contains(strings.ToLower(groupVals[0]), "jibaketa") {
		t.Fatalf("expected jibaketa group extracted, got=%v", groupVals)
	}
}

func TestGroupSlot2PlatformInfer_JibaketaDistributorNoPlatform(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[jibaketa合成&音頻壓制][代理商粵語] Sample Show - 01 [WEB-DL][1080p]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if hasAttrValue(attrValues(res, model.AttrPlatform), "代理商粵語") {
		t.Fatalf("distributor slot must not be emitted as platform, got=%v", attrValues(res, model.AttrPlatform))
	}
	if !hasAttrValue(attrValues(res, model.AttrAudioLang), "YUE") {
		t.Fatalf("expected audio_lang YUE, got=%v", attrValues(res, model.AttrAudioLang))
	}
}

func TestGroupSlot2PlatformInfer_JibaketaUnknownPlatformInfer(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[jibaketa合成&音頻壓制][MyTV粵語] Sample Show - 01 [WEB-DL][1080p]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !hasAttrValue(attrValues(res, model.AttrPlatform), "MYTV") {
		t.Fatalf("expected inferred platform MYTV, got=%v", attrValues(res, model.AttrPlatform))
	}
	if !hasAttrValue(attrValues(res, model.AttrAudioLang), "YUE") {
		t.Fatalf("expected audio_lang YUE, got=%v", attrValues(res, model.AttrAudioLang))
	}
}

func TestLexiconDict_ConfigFirst_GluedSourceResolutionVariants(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	cases := []struct {
		name       string
		raw        string
		wantSource string
		wantRes    string
	}{
		{
			name:       "web720p",
			raw:        "[TestGroup] Sample Show - 01 [WEB720P][AAC][MKV]",
			wantSource: "WEBRip",
			wantRes:    "720p",
		},
		{
			name:       "web2160p",
			raw:        "[TestGroup] Sample Show - 01 [WEB2160P][AAC][MKV]",
			wantSource: "WEBRip",
			wantRes:    "2160p",
		},
		{
			name:       "bd720p",
			raw:        "[TestGroup] Sample Show - 01 [BD720P][AAC][MKV]",
			wantSource: "BD",
			wantRes:    "720p",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := p.Parse(tc.raw)
			if err != nil {
				t.Fatalf("parse failed: %v", err)
			}
			if !hasAttrValue(attrValues(res, model.AttrSourceType), tc.wantSource) {
				t.Fatalf("source_type got=%v want=%q", attrValues(res, model.AttrSourceType), tc.wantSource)
			}
			if !hasAttrValue(attrValues(res, model.AttrResolution), tc.wantRes) {
				t.Fatalf("resolution got=%v want=%q", attrValues(res, model.AttrResolution), tc.wantRes)
			}
		})
	}
}

func TestParserTechCombo_BitDepthWithTrailingComma(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Gecko] Kaijyu Sekai Seifuku - S01E47 [ABEMA.WEB-DL 1920x1080 AVC 8bit, AAC 2.0][819BADDB].mkv"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !hasAttrValue(attrValues(res, model.AttrBitDepth), "8bit") {
		t.Fatalf("expected bit_depth=8bit, got=%v", attrValues(res, model.AttrBitDepth))
	}
	if !hasAttrValue(attrValues(res, model.AttrCRC), "819BADDB") {
		t.Fatalf("expected crc=819BADDB, got=%v", attrValues(res, model.AttrCRC))
	}
	for _, tok := range res.Tokens {
		if strings.EqualFold(strings.TrimSpace(tok.Text), "819BADDB") && !tok.Removed {
			t.Fatalf("crc token should be consumed, tokens=%v", res.Tokens)
		}
	}
}

func TestLexiconDict_ConfigFirst_ITABDCompound(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "Mahou Shoujo Madoka Magica S1 ITA-BD x265 10bit"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if canonicalTitle(res) != "Mahou Shoujo Madoka Magica" {
		t.Fatalf("title polluted, got=%q", canonicalTitle(res))
	}
	if !hasAttrValue(attrValues(res, model.AttrPlatformRegion), "ITA") {
		t.Fatalf("expected platform_region=ITA, got=%v", attrValues(res, model.AttrPlatformRegion))
	}
	if !hasAttrValue(attrValues(res, model.AttrSourceType), "BD") {
		t.Fatalf("expected source_type includes BD, got=%v", attrValues(res, model.AttrSourceType))
	}
}

func TestParserPlatformRegion_EmbeddedRestrictionDoesNotAnchorTitleSpan(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[ANi] Akiba Maid War - 秋葉原冥途戰爭（僅限港澳台地區） - 02 [1080P][Bilibili][WEB-DL][AAC AVC][CHT CHS][MP4]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !hasAttrValue(attrValues(res, model.AttrPlatformRegion), "HK+MO+TW") {
		t.Fatalf("expected platform_region=HK+MO+TW, got=%v", attrValues(res, model.AttrPlatformRegion))
	}
	found := false
	for _, a := range res.Attributes {
		if a.Type != model.AttrPlatformRegion {
			continue
		}
		if strings.TrimSpace(a.Normalized) != "HK+MO+TW" {
			continue
		}
		found = true
		if a.TokenStart != -1 || a.TokenEnd != -1 {
			t.Fatalf("embedded region restriction should not keep title-span indices, got token_start=%d token_end=%d", a.TokenStart, a.TokenEnd)
		}
	}
	if !found {
		t.Fatalf("missing platform_region attribute for embedded restriction")
	}
	if strings.Contains(canonicalTitle(res), "港澳台") || strings.Contains(canonicalTitle(res), "港澳臺") {
		t.Fatalf("region restriction leaked into title, title=%q", canonicalTitle(res))
	}
}

func TestLexiconDict_ConfigFirst_BDBoxAtomicNoAliasPollution(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[VCB-Studio] Some Title [BD-BOX][1080p][x265][10bit]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !hasAttrValue(attrValues(res, model.AttrSourceType), "BD") {
		t.Fatalf("expected source_type includes BD, got=%v", attrValues(res, model.AttrSourceType))
	}
	for _, a := range canonicalAliases(res) {
		if strings.EqualFold(strings.TrimSpace(a), "BOX") {
			t.Fatalf("bd-box token leaked as alias: %v", canonicalAliases(res))
		}
	}
}

func TestParserAliasCleanup_TrimTailAbsoluteEpisodeWhenSeasonRelativePrimary(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[WF] Sousou no Frieren 2nd Season - 04 [WEBRip 1080p x265 AAC][DUAL][PT-BR] (Frieren: Beyond Journey's End - 32)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalEpisode(res); got != "4" {
		t.Fatalf("episode=%q want=4", got)
	}
	aliases := canonicalAliases(res)
	if !containsAlias(aliases, "Frieren Beyond Journey's End") {
		t.Fatalf("expected cleaned alias, got=%v", aliases)
	}
	for _, a := range aliases {
		if strings.Contains(a, "32") {
			t.Fatalf("tail absolute episode should be trimmed from alias, got=%v", aliases)
		}
	}
}

func TestJibaketaYueSample_SubtitleAndPlatformCleanup(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[jibaketa合成][代理商粵語]SPY×FAMILY間諜家家酒 / 间谍过家家Season 3 - 09 [粵日雙語+內封繁體中文字幕](WEB 1920x1080 AVC AACx2 SRT MUSE CHT)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !hasAttrValue(attrValues(res, model.AttrAudioLang), "YUE") {
		t.Fatalf("expected audio_lang YUE, got=%v", attrValues(res, model.AttrAudioLang))
	}
	if !hasAttrValue(attrValues(res, model.AttrSubLang), "TC") {
		t.Fatalf("expected sub_lang TC, got=%v", attrValues(res, model.AttrSubLang))
	}
	if hasAttrValue(attrValues(res, model.AttrSubLang), "JPN") {
		t.Fatalf("unexpected sub_lang JPN from 粵日雙語, got=%v", attrValues(res, model.AttrSubLang))
	}
	if !hasAttrValue(attrValues(res, model.AttrPlatform), "MUSE") {
		t.Fatalf("expected platform MUSE, got=%v", attrValues(res, model.AttrPlatform))
	}
	for _, a := range canonicalAliases(res) {
		if strings.HasSuffix(strings.ToLower(strings.TrimSpace(a)), "season") {
			t.Fatalf("season residue leaked in alias: %v", canonicalAliases(res))
		}
	}
}

func TestGeckoGTETX_ParenAliasBetweenEpisodeAndTech(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Gecko] Kaijyu Sekai Seifuku - S01E47 (かいじゅうせかいせいふく; Kaijuu Sekai Seifuku; Kaijuu Sekai Seifuku (TV)) [ABEMA.WEB-DL 1920x1080 AVC 8bit, AAC 2.0][819BADDB]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if canonicalTitle(res) != "Kaijyu Sekai Seifuku" {
		t.Fatalf("unexpected title=%q", canonicalTitle(res))
	}
	aliases := canonicalAliases(res)
	if !containsAlias(aliases, "かいじゅうせかいせいふく") {
		t.Fatalf("expected kana alias, got=%v", aliases)
	}
	if !containsAlias(aliases, "Kaijuu Sekai Seifuku") {
		t.Fatalf("expected latin alias, got=%v", aliases)
	}
}

func TestUnderscoreSceneStyle_NoRouteNeeded(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[WZF]One_Punch_Man_(2025)_-_Ep_07~08[HI10p][1920x1080][X264-AAC][WEB][Sub_Esp]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if canonicalTitle(res) != "One Punch Man" {
		t.Fatalf("unexpected title=%q", canonicalTitle(res))
	}
	if got := canonicalEpisodeRange(res); got != "7-8" {
		t.Fatalf("episode_range=%q want=7-8", got)
	}
	if !hasAttrValue(attrValues(res, model.AttrSubLang), "SPA") {
		t.Fatalf("expected sub_lang SPA, got=%v", attrValues(res, model.AttrSubLang))
	}
	if !hasAttrValue(attrValues(res, model.AttrBitDepth), "10bit") {
		t.Fatalf("expected bit_depth 10bit, got=%v", attrValues(res, model.AttrBitDepth))
	}
	for _, tok := range res.Tokens {
		if strings.EqualFold(strings.TrimSpace(tok.Text), "HI10p") && !tok.Removed {
			t.Fatalf("HI10p token should be consumed, tokens=%v", res.Tokens)
		}
	}
}

func TestLexiconDict_FHDTokenMappedAndConsumed(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[TestGroup] Sample Title - 01 [FHD][WEB-DL][AAC AVC]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !hasAttrValue(attrValues(res, model.AttrResolution), "1080p") {
		t.Fatalf("expected resolution 1080p, got=%v", attrValues(res, model.AttrResolution))
	}
	for _, tok := range res.Tokens {
		if strings.EqualFold(strings.TrimSpace(tok.Text), "FHD") && !tok.Removed {
			t.Fatalf("FHD token should be consumed, tokens=%v", res.Tokens)
		}
	}
}

func TestLexiconDict_4KHDRComboShouldNotPolluteTitle(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[4K影视屋][4K_HDR][007：无暇赴死No.Time.to.Die.2021][2160P][WEB-DL][杜比全景声][HEVC]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if strings.EqualFold(strings.TrimSpace(canonicalTitle(res)), "4K HDR") {
		t.Fatalf("4k_hdr block leaked to title: %q", canonicalTitle(res))
	}
	if !hasAttrValue(attrValues(res, model.AttrResolution), "2160p") {
		t.Fatalf("expected resolution 2160p, got=%v", attrValues(res, model.AttrResolution))
	}
	if !hasAttrValue(attrValues(res, model.AttrHDR), "HDR") {
		t.Fatalf("expected hdr=HDR from 4k_hdr combo, got=%v", attrValues(res, model.AttrHDR))
	}
}

func TestLexiconDict_DVTokenToDolbyVisionHDR(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[TestGroup] Sample Show - 01 [DV][WEB-DL][2160p][HEVC]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !hasAttrValue(attrValues(res, model.AttrHDR), "DOLBY_VISION") {
		t.Fatalf("expected hdr DOLBY_VISION, got=%v", attrValues(res, model.AttrHDR))
	}
}

func TestLexiconDict_GluedTechTokensShouldBeConsumed(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	cases := []struct {
		name        string
		raw         string
		token       string
		expectAttrs map[model.AttrType]string
	}{
		{
			name:  "uhdbdrip",
			raw:   "[4K_HDR][DBD-Raws][剧场版 鬼灭之刃 无限列车篇/Kimetsu no Yaiba The Movie Mugen Ressha Hen][2160P][UHDBDRip][HEVC-10bit][简繁外挂][FLAC][MKV]",
			token: "UHDBDRip",
			expectAttrs: map[model.AttrType]string{
				model.AttrSourceType: "BDRip",
				model.AttrEdition:    "UHD_BDRIP",
			},
		},
		{
			name:  "uhdrip",
			raw:   "[AI-Raws] Future Boy Conan 未来少年コナン UHDRip 2160p MKV",
			token: "UHDRip",
			expectAttrs: map[model.AttrType]string{
				model.AttrSourceType: "HDRip",
				model.AttrEdition:    "UHDRIP",
			},
		},
		{
			name:  "10bits",
			raw:   "Aldnoah.Zero (2014) VOSTFR 1080p 10bits BluRay x265-GundamGuy",
			token: "10bits",
			expectAttrs: map[model.AttrType]string{
				model.AttrBitDepth: "10bit",
			},
		},
		{
			name:  "hdtv720p",
			raw:   "[PorterRAWS][殺手廢J/Killing Procedures][全8集+特別篇][HDTV720P][粵語中字]",
			token: "HDTV720P",
			expectAttrs: map[model.AttrType]string{
				model.AttrSourceType: "HDTV",
				model.AttrResolution: "720p",
			},
		},
		{
			name:  "dvd480p",
			raw:   "[PorterRAWS] 火影忍者.NARUTO[E01-150][DVD480P][粤日国三语][HALFCD-NORM]",
			token: "DVD480P",
			expectAttrs: map[model.AttrType]string{
				model.AttrSourceType: "DVD",
				model.AttrResolution: "480p",
			},
		},
		{
			name:  "hdtc1080p",
			raw:   "[PorterRAWS][AI超分画质]数码宝贝02：起源/数码暴龙/ デジモンアドベンチャー02 THE BEGINNING[HDTC1080P][TearsHD][粤语中字]",
			token: "HDTC1080P",
			expectAttrs: map[model.AttrType]string{
				model.AttrSourceType: "HDTC",
				model.AttrResolution: "1080p",
			},
		},
		{
			name:  "bd2160p",
			raw:   "[PorterRAWS][生化危机：死亡岛2023/ バイオハザード：デスアイランド][BD2160P][英语中字]",
			token: "BD2160P",
			expectAttrs: map[model.AttrType]string{
				model.AttrSourceType: "BD",
				model.AttrResolution: "2160p",
			},
		},
		{
			name:  "dvd576p",
			raw:   "[PorterRAWS]史上最大作弊战争 / 出猫斗一番 / That's Cunning! 史上最大作弊战争[DVD576P][日语中字]",
			token: "DVD576P",
			expectAttrs: map[model.AttrType]string{
				model.AttrSourceType: "DVD",
				model.AttrResolution: "576p",
			},
		},
		{
			name:  "360p",
			raw:   "[PorterRAWS]去吧！稻中桌球社 / 去吧！稻中乒團/ Ping Pong Club[360P][VCDRip][粤语中字]",
			token: "360P",
			expectAttrs: map[model.AttrType]string{
				model.AttrResolution: "360p",
			},
		},
		{
			name:  "xvid",
			raw:   "Beet The Vandel Buster - WEBDL - VOSTFR - Xvid MP3 - ETF",
			token: "Xvid",
			expectAttrs: map[model.AttrType]string{
				model.AttrVideoCodec: "XVID",
			},
		},
		{
			name:  "8bits",
			raw:   "[WZF]FullMetal_Alchemist_-_Milos_no_Sei-Naru_Hoshi_(2011)[BD-1080p][8bits][x264-FLAC_6ch][1920x1080][Sub_Esp].mkv",
			token: "8bits",
			expectAttrs: map[model.AttrType]string{
				model.AttrBitDepth: "8bit",
			},
		},
		{
			name:  "2276p",
			raw:   "[A&C] 足球小将剧场版1.Captain Tsubasa Movie 01 The great competition of Europe (1985) (35mm 2276p HEVC) [Multi-Audio-Subs](原胶片数码扫描转制高清4K版）",
			token: "2276p",
			expectAttrs: map[model.AttrType]string{
				model.AttrResolution: "2276p",
			},
		},
		{
			name:  "bd1080p",
			raw:   "[DB] Dandadan 2nd Season | Dan Da Dan Season 2 [Dual Audio 10bit BD1080p][HEVC-x265]",
			token: "BD1080p",
			expectAttrs: map[model.AttrType]string{
				model.AttrSourceType: "BD",
				model.AttrResolution: "1080p",
			},
		},
		{
			name:  "ddp5_1",
			raw:   "100 Meters 2025 1080p NF WEB-DL DUAL DDP5.1 H 264-VARYG (Hyakuemu., Dual-Audio, Multi-Subs)",
			token: "DDP5",
			expectAttrs: map[model.AttrType]string{
				model.AttrAudioCodec:    "EAC3",
				model.AttrAudioChannels: "5.1",
			},
		},
		{
			name:  "dv_hdr10p",
			raw:   "Chainsaw.Man.The.Movie.Reze.Arc.2025.REPACK.2160p.MA.WEB-DL.DUAL.DDP5.1.Atmos.DV.HDR10P.H.265-BYNDR",
			token: "DV",
			expectAttrs: map[model.AttrType]string{
				model.AttrHDR:        "DOLBY_VISION",
				model.AttrAudioCodec: "EAC3",
			},
		},
		{
			name:  "hdr10p",
			raw:   "Chainsaw.Man.The.Movie.Reze.Arc.2025.REPACK.2160p.MA.WEB-DL.DUAL.DDP5.1.Atmos.DV.HDR10P.H.265-BYNDR",
			token: "HDR10P",
			expectAttrs: map[model.AttrType]string{
				model.AttrHDR: "HDR10_PLUS",
			},
		},
		{
			name:  "dvdiso",
			raw:   "Dofus.livre.1.Julith.2016.DVDISO.FRENCH-MaitreAnonyme.iso",
			token: "DVDISO",
			expectAttrs: map[model.AttrType]string{
				model.AttrSourceType: "DVD",
			},
		},
		{
			name:  "dvd9",
			raw:   "Dragon Ball (TV Series 1986–1989) DVD9 RETAIL",
			token: "DVD9",
			expectAttrs: map[model.AttrType]string{
				model.AttrSourceType: "DVD",
				model.AttrEdition:    "DVD9",
			},
		},
		{
			name:  "jdvd",
			raw:   "(WHIM) Deltora Quest (2007) [480p JDVD] [H265] [Opus 2.0]",
			token: "JDVD",
			expectAttrs: map[model.AttrType]string{
				model.AttrSourceType: "DVD",
				model.AttrEdition:    "JDVD",
			},
		},
		{
			name:  "uhd_note",
			raw:   "Dragon Ball DAIMA 2024 S01 Deluxe Edition JPN 2160p UHD Blu-ray 1080p Blu-ray BOX-GWXHBL [BDMV]",
			token: "UHD",
			expectAttrs: map[model.AttrType]string{
				model.AttrEdition: "UHD",
			},
		},
		{
			name:  "atmos_token",
			raw:   "Chainsaw Man The Movie Reze Arc 2025 1080p AMZN WEB-DL MULTi DDP5.1 Atmos H 264-VARYG (Chainsaw Man: Reze-hen, Multi-Audio, Multi-Subs)",
			token: "Atmos",
			expectAttrs: map[model.AttrType]string{
				model.AttrAudioObject: "ATMOS",
			},
		},
		{
			name:  "ray1080p_glued",
			raw:   "Origine 2006 MULTI Blu-Ray1080p HEVC DTS-HDMA 5.1 - MaitreAnonyme",
			token: "Ray1080p",
			expectAttrs: map[model.AttrType]string{
				model.AttrSourceType: "BluRay",
				model.AttrResolution: "1080p",
			},
		},
		{
			name:  "flac2",
			raw:   "Angels.Egg.1985.1080p.BluRay.FLAC2.0.Hi10P.x264-ZoroSenpai.mkv",
			token: "FLAC2",
			expectAttrs: map[model.AttrType]string{
				model.AttrAudioCodec:    "FLAC",
				model.AttrAudioChannels: "2.0",
			},
		},
		{
			name:  "aac5",
			raw:   "Chainsaw.Man.The.Movie.Reze.Arc.2025.1080p.AMZN.WEBRip.Dual.Audio.AAC5.1.10bits.x265-Rapta",
			token: "AAC5",
			expectAttrs: map[model.AttrType]string{
				model.AttrAudioCodec:    "AAC",
				model.AttrAudioChannels: "5.1",
			},
		},
		{
			name:  "dts5",
			raw:   "The Legend of Hei 2 / Luo Xiaohei Zhanji 2 (2025) [2160p WEB-DL H265 HQ HDR DTS5.1]",
			token: "DTS5",
			expectAttrs: map[model.AttrType]string{
				model.AttrAudioCodec:    "DTS",
				model.AttrAudioChannels: "5.1",
			},
		},
		{
			name:  "eac3_aac_combo",
			raw:   "[Prejudice-Studio] 我们不可能成为恋人！绝对不行。〜再次闪耀！〜 Watanare 〜Next Sunshine〜 - OVA02 [WebRip 1080P HEVC 10bit EAC3&AAC MKV][简繁内封]",
			token: "EAC3&AAC",
			expectAttrs: map[model.AttrType]string{
				model.AttrAudioCodec: "EAC3",
			},
		},
		{
			name:  "ac3_truehd_combo",
			raw:   "[DBD-Raws][龙珠Z/Dragonball Z/ドラゴンボール Z][S3][075-107+特典映像][1080P][BDRip][HEVC-10bit][AC3&TrueHD][MKV]",
			token: "AC3&TrueHD",
			expectAttrs: map[model.AttrType]string{
				model.AttrAudioCodec: "TRUEHD",
			},
		},
		{
			name:  "aac_ac3_comma_combo",
			raw:   "[Anime Time] Dragon Ball Z Complete Series (Colour Corrected) [DVD] [Dual Audio] [480p][HEVC 10bit x265][AAC,AC3][Eng Sub] [Batch]",
			token: "AAC,AC3",
			expectAttrs: map[model.AttrType]string{
				model.AttrAudioCodec: "AC3",
			},
		},
		{
			name:  "ac3_aac_comma_combo",
			raw:   "[Anime Time] Tate No Yūsha No Nariagari (The Rising Of The Shield Hero) (Season 1,2,3) [BD] [1080p][HEVC 10bit x265][AC3,AAC][Dual Audio][ENG Subs] [Batch]",
			token: "AC3,AAC",
			expectAttrs: map[model.AttrType]string{
				model.AttrAudioCodec: "AAC",
			},
		},
		{
			name:  "aac_atmos_combo",
			raw:   "[EuphoniumSub] 超级马力欧兄弟大电影 / The Super Mario Bros. Movie [Webrip][1080p][HEVC_AAC&Atmos][中英双语]",
			token: "AAC&Atmos",
			expectAttrs: map[model.AttrType]string{
				model.AttrAudioCodec:  "AAC",
				model.AttrAudioObject: "ATMOS",
			},
		},
		{
			name:  "flac5",
			raw:   "[MOMIJI] Detective Conan: One-eyed Flashback (2025) (BD REMUX 1080p AVC FLAC 5.1) | Detective.Conan.One-eyed.Flashback.2025.1080p.BluRay.Remux.AVC.FLAC5.1-MOMIJI.mkv",
			token: "FLAC5",
			expectAttrs: map[model.AttrType]string{
				model.AttrAudioCodec:    "FLAC",
				model.AttrAudioChannels: "5.1",
			},
		},
		{
			name:  "flac_trailing_comma",
			raw:   "[MirrorNova] Doraemon - Nobita and the Steel Troops (1986) [1080p WEB-DL, Dual-Audio, FLAC, AVC]",
			token: "FLAC,",
			expectAttrs: map[model.AttrType]string{
				model.AttrAudioCodec: "FLAC",
			},
		},
		{
			name:  "aac_times_2",
			raw:   "[北宇治字幕组] 蓦然回首 / Look Back [Movie v2][WebRip][HEVC_AAC×2][简繁日内封]",
			token: "AAC×2",
			expectAttrs: map[model.AttrType]string{
				model.AttrAudioCodec: "AAC",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := p.Parse(tc.raw)
			if err != nil {
				t.Fatalf("parse failed: %v", err)
			}
			for typ, want := range tc.expectAttrs {
				if !hasAttrValue(attrValues(res, typ), want) {
					t.Fatalf("expected %s=%s, got=%v", typ, want, attrValues(res, typ))
				}
			}
			for _, tok := range res.Tokens {
				if strings.EqualFold(strings.TrimSpace(tok.Text), tc.token) && !tok.Removed {
					t.Fatalf("token %q should be consumed, tokens=%v", tc.token, res.Tokens)
				}
			}
		})
	}
}

func hasAttrValue(got []string, want string) bool {
	for _, v := range got {
		if strings.EqualFold(strings.TrimSpace(v), strings.TrimSpace(want)) {
			return true
		}
	}
	return false
}

func containsAlias(aliases []string, want string) bool {
	for _, a := range aliases {
		if strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(want)) {
			return true
		}
	}
	return false
}

func TestNormalizeAliasCandidateForOutput_StripsInlineLangMetaWords(t *testing.T) {
	p := &Parser{
		aliasInlineStripWords: map[string]struct{}{
			"vostfr": {},
			"vosten": {},
		},
	}
	in := "Maria Watches Over Us Printemps VOSTFR VOSTEN マリア様がみてる ～春～"
	got := p.normalizeAliasCandidateForOutput(in)
	if strings.Contains(strings.ToLower(got), "vostfr") || strings.Contains(strings.ToLower(got), "vosten") {
		t.Fatalf("inline lang markers should be stripped, got=%q", got)
	}
	if !strings.Contains(got, "Maria Watches Over Us Printemps") {
		t.Fatalf("core alias text should be kept, got=%q", got)
	}
	if !strings.Contains(got, "マリア様がみてる") {
		t.Fatalf("japanese alias text should be kept, got=%q", got)
	}
}

func TestExtractParenContentMarkers(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{in: "Movie", want: []string{"MOVIE"}},
		{in: "Movies + Specials", want: []string{"MOVIE", "SPECIAL"}},
		{in: "Season 1-3 + Movie", want: []string{"TV", "MOVIE"}},
		{in: "3 + Movie", want: []string{"MOVIE"}},
	}
	for _, tc := range cases {
		got := extractParenContentMarkers(tc.in)
		for _, w := range tc.want {
			if !containsAlias(got, w) {
				t.Fatalf("input=%q markers=%v want include=%q", tc.in, got, w)
			}
		}
	}
}
