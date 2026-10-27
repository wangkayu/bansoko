package templatefacts

import (
	"fmt"
	"sort"
	"strings"
)

// Scenario is one template kind that reads facts. A fact lists the scenarios
// whose producer fills it; it is never advertised to a scenario that cannot
// supply it.
type Scenario uint8

const (
	ScenarioNaming Scenario = 1 << iota
	ScenarioDownloadSubmitted
	ScenarioFileCompleted
	ScenarioFileFailed
	// The workflow triggers: a release first seen for a subscribed anime, a
	// failed download, a subscription transition and unknown files an
	// acquisition wrote to the library.
	ScenarioReleaseDiscovered
	ScenarioDownloadFailed
	ScenarioSubscriptionChanged
	ScenarioUnknownImported
)

const (
	scenarioFile = ScenarioFileCompleted | ScenarioFileFailed
	// scenarioMedia is the media pipeline triggers.
	scenarioMedia = ScenarioDownloadSubmitted | scenarioFile
	// scenarioWorkflow is the workflow triggers; they read only the facts
	// that list them.
	scenarioWorkflow = ScenarioReleaseDiscovered | ScenarioDownloadFailed | ScenarioSubscriptionChanged | ScenarioUnknownImported
	scenarioNotify   = scenarioMedia | scenarioWorkflow
	scenarioAll      = ScenarioNaming | scenarioMedia
	// scenarioRelease is the workflow triggers that concern one release.
	scenarioRelease = ScenarioReleaseDiscovered | ScenarioDownloadFailed | ScenarioUnknownImported
)

// Scenarios lists every scenario in registry order.
var Scenarios = []Scenario{ScenarioNaming, ScenarioDownloadSubmitted, ScenarioFileCompleted, ScenarioFileFailed,
	ScenarioReleaseDiscovered, ScenarioDownloadFailed, ScenarioSubscriptionChanged, ScenarioUnknownImported}

// Phase is when a fact becomes available.
type Phase string

const (
	// PhaseRelease facts are fixed when the release is accepted.
	PhaseRelease Phase = "release"
	// PhaseExecution facts exist only once the file is processed; previews
	// show them as placeholders.
	PhaseExecution Phase = "execution"
)

// Type is the template value type of a fact.
type Type string

const (
	TypeString    Type = "string"
	TypeInteger   Type = "integer"
	TypeTimestamp Type = "timestamp"
	TypeBoolean   Type = "boolean"
	TypeList      Type = "list"
)

// Text is one piece of catalog copy in both shipped languages.
type Text struct{ ZH, EN string }

// Provenance is the value-source class of a registered fact: where its value
// comes from in the code that fills the FactSet slot.
type Provenance string

const (
	// ProvenanceCollected: a stored value (feed, seed, provider API, owner
	// input, file inspection, the download client) — read, never computed.
	ProvenanceCollected Provenance = "collected"
	// ProvenanceParsed: the parser's output from the release title, the file
	// name text, or the torrent's bencode payload.
	ProvenanceParsed Provenance = "parsed"
	// ProvenanceDerived: a system composite whose parts all come from a
	// source (a chain the resolver or a projection computes from other
	// facts). The value is rebuilt, but every word in it exists upstream or
	// is part of the documented vocabulary: an episode number formatted as
	// S01E07, a season folder the resolver assembles, a parser token, or a
	// naming-language word this project defines on purpose (jpsc, 简日, 内封).
	ProvenanceDerived Provenance = "derived"
	// ProvenanceInvented flags unauthorized invention: a value the code can
	// emit that is neither supplied by the fact's source nor a word the
	// contract defines. It covers a fallback constant standing in for a
	// missing source, a foreign source silently substituting (a resolution
	// read from the release title on a media fact), and sample data reaching
	// a real render. A deliberate vocabulary word is derived, not invented;
	// this class exists to make an undeclared value impossible to miss.
	ProvenanceInvented Provenance = "invented"
)

// Fact is one registered template fact. The registry is the single source of
// the naming and notification catalogs, their render contexts and the
// missing-fact check.
type Fact struct {
	Path  string
	Type  Type
	Phase Phase
	// Provenance is the fact's value-source class; every registered fact
	// carries exactly one and the catalog copy leads with it.
	Provenance  Provenance
	Label       Text
	Description Text
	// Scenarios lists the template kinds whose producer fills the fact;
	// Nullable the subset where the frozen value may be absent.
	Scenarios Scenario
	Nullable  Scenario
	// Hidden facts render but are not advertised (aliases only).
	Hidden bool
	// Sample is the catalog sample value; samples overrides it per scenario.
	Sample  any
	samples map[Scenario]any
	get     func(*FactSet) any
}

// In reports whether the fact is available to the scenario.
func (f Fact) In(s Scenario) bool { return f.Scenarios&s != 0 }

// NullableIn reports whether the fact may be absent in the scenario.
func (f Fact) NullableIn(s Scenario) bool { return f.Nullable&s != 0 }

// Root is the first path segment.
func (f Fact) Root() string { root, _, _ := strings.Cut(f.Path, "."); return root }

// LabelKey and DescriptionKey are the UI copy keys of the fact.
func (f Fact) LabelKey() string { return "facts." + f.Path + ".label" }

func (f Fact) DescriptionKey() string { return "facts." + f.Path + ".description" }

// SampleFor returns the sample value for one scenario.
func (f Fact) SampleFor(s Scenario) any {
	for scenario, value := range f.samples {
		if scenario&s != 0 {
			return value
		}
	}
	return f.Sample
}

// Value returns the fact's value in fs: nil when absent (blank string, nil
// pointer or nil slice), never a substitute.
func (f Fact) Value(fs *FactSet) any {
	if fs == nil || f.get == nil {
		return nil
	}
	return normalize(f.get(fs))
}

func normalize(value any) any {
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		return typed
	case *int64:
		if typed == nil {
			return nil
		}
		return *typed
	case []string:
		if typed == nil {
			return nil
		}
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = item
		}
		return out
	case *bool:
		if typed == nil {
			return nil
		}
		return *typed
	case []map[string]any:
		if typed == nil {
			return nil
		}
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = item
		}
		return out
	default:
		return typed
	}
}

// Absent reports whether a normalized value counts as missing: null or a
// blank string.
func Absent(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(typed) == ""
	}
	return false
}

// FormatSample renders a sample value as catalog text.
func FormatSample(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []string:
		return strings.Join(typed, ", ")
	case []map[string]any:
		paths := make([]string, 0, len(typed))
		for _, item := range typed {
			paths = append(paths, fmt.Sprint(item["path"]))
		}
		return strings.Join(paths, ", ")
	default:
		return fmt.Sprint(typed)
	}
}

// RootVars is the user Vars namespace: vars.<group>.<name>, dynamic keys.
const RootVars = "vars"

// Reserved roots: readable names with no fields. `info` used to be reserved
// while media.hdr carried the inspected media information (naming spec §2.4);
// the media facts moved under the media root, so nothing is fieldless now.
var ReservedRoots = []string{}

// ScenarioFacts returns the registered facts of one scenario in registry order,
// including hidden ones.
func ScenarioFacts(s Scenario) []Fact {
	out := make([]Fact, 0, len(registry))
	for _, fact := range registry {
		if fact.In(s) {
			out = append(out, fact)
		}
	}
	return out
}

// All returns every registered fact in registry order.
func All() []Fact { return append([]Fact(nil), registry...) }

// Lookup returns one registered fact by path.
func Lookup(path string) (Fact, bool) {
	fact, ok := registryIndex[path]
	return fact, ok
}

// Roots returns the roots one scenario reads, sorted, plus the reserved roots
// for naming.
func Roots(s Scenario) []string {
	seen := map[string]bool{}
	for _, fact := range ScenarioFacts(s) {
		seen[fact.Root()] = true
	}
	if s&ScenarioNaming != 0 {
		for _, root := range ReservedRoots {
			seen[root] = true
		}
	}
	// vars has dynamic keys (the frozen Vars output) and is readable in
	// every scenario.
	seen[RootVars] = true
	roots := make([]string, 0, len(seen))
	for root := range seen {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	return roots
}

// FlatValues returns path -> value for every fact of the scenario.
func FlatValues(s Scenario, fs *FactSet) map[string]any {
	out := map[string]any{}
	for _, fact := range ScenarioFacts(s) {
		out[fact.Path] = fact.Value(fs)
	}
	return out
}

// ScenarioObject returns the nested template object of one scenario: every fact is
// present (null when absent) so a conditional read of a missing value sees
// null; reserved roots are empty objects; `vars` carries the frozen Vars and
// is left out when there are none (the naming wrapper then binds an empty
// object).
func ScenarioObject(s Scenario, fs *FactSet) map[string]any {
	out := map[string]any{}
	for _, fact := range ScenarioFacts(s) {
		segments := strings.Split(fact.Path, ".")
		node := out
		for _, segment := range segments[:len(segments)-1] {
			child, ok := node[segment].(map[string]any)
			if !ok {
				child = map[string]any{}
				node[segment] = child
			}
			node = child
		}
		value := fact.Value(fs)
		if value == nil && fact.Type == TypeString && !fact.NullableIn(s) && fs != nil {
			// A fact the scenario declares always present is never null: an
			// empty value (an extensionless file's ext) is the empty string.
			value = ""
		}
		node[segments[len(segments)-1]] = value
	}
	if s&ScenarioNaming != 0 {
		for _, root := range ReservedRoots {
			out[root] = map[string]any{}
		}
		vars := map[string]any{}
		if fs != nil && len(fs.Vars) > 0 {
			for group, values := range fs.Vars {
				inner := make(map[string]any, len(values))
				for key, value := range values {
					inner[key] = value
				}
				vars[group] = inner
			}
			out["vars"] = vars
		}
	}
	return out
}

// SampleFlat returns path -> sample for every advertised fact of the scenario.
func SampleFlat(s Scenario) map[string]any {
	out := map[string]any{}
	for _, fact := range ScenarioFacts(s) {
		if fact.Hidden {
			continue
		}
		value := fact.SampleFor(s)
		if list, ok := value.([]string); ok {
			value = normalize(list)
		}
		out[fact.Path] = value
	}
	return out
}

var registryIndex = func() map[string]Fact {
	index := make(map[string]Fact, len(registry))
	for _, fact := range registry {
		if _, dup := index[fact.Path]; dup {
			panic("templatefacts: duplicate fact " + fact.Path)
		}
		switch fact.Provenance {
		case ProvenanceCollected, ProvenanceParsed, ProvenanceDerived, ProvenanceInvented:
		default:
			panic("templatefacts: fact without a provenance class " + fact.Path)
		}
		index[fact.Path] = fact
	}
	return index
}()

// fact registers one fact. Every fact is collected unless the registration
// marks it parsed or derived with the .parsed() / .derived() chains, so a new
// registration without a marker can only mean a stored value; the registry
// index rejects anything that ends up unmarked anyway.
func fact(path string, typ Type, phase Phase, scenarios, nullable Scenario, label, description Text, sample any, get func(*FactSet) any) Fact {
	return Fact{Path: path, Type: typ, Phase: phase, Provenance: ProvenanceCollected, Scenarios: scenarios, Nullable: nullable, Label: label, Description: description, Sample: sample, get: get}
}

// parsed marks the fact's value as the parser's output from the release
// title or the file name text.
func (f Fact) parsed() Fact { f.Provenance = ProvenanceParsed; return f }

// derived marks the fact's value as a system composite the resolver or a
// projection computes from other facts, every part of it traceable upstream.
func (f Fact) derived() Fact { f.Provenance = ProvenanceDerived; return f }

// invented flags unauthorized invention: the fact's producer can emit a value
// that neither its source supplies nor the contract defines. Naming-language
// vocabulary (jpsc, 简日, 内封, Season 01) is defined on purpose and stays
// derived; this marker is for a source the fact's namespace must not read, a
// constant standing in for a missing value, or sample data reaching a render.
func (f Fact) invented() Fact { f.Provenance = ProvenanceInvented; return f }

func (f Fact) with(s Scenario, sample any) Fact {
	if f.samples == nil {
		f.samples = map[Scenario]any{}
	}
	f.samples[s] = sample
	return f
}

func (f Fact) hidden() Fact { f.Hidden = true; return f }

const (
	sampleTitle    = "进击的巨人"
	sampleYear     = "2025"
	sampleRelease  = "[示例字幕组] 进击的巨人 - 07 [WebRip 1080p HEVC-10bit AAC][CHS]"
	sampleFile     = "[示例字幕组] 进击的巨人 - 07 [WebRip 1080p HEVC-10bit AAC].mkv"
	sampleFileStem = "[示例字幕组] 进击的巨人 - 07 [WebRip 1080p HEVC-10bit AAC]"
	// The sample library tree under the mount: the default rules render
	// `<kind>/<title> <year>/<season folder>` and `<title> <SxxEyy>.<ext>`.
	sampleOutDir     = "tv/进击的巨人 2025/Season 01"
	sampleOutName    = "进击的巨人 S01E07.mkv"
	sampleFileSize   = int64(734003200)
	sampleOccurredAt = "2026-07-01T12:30:00Z"
)

var registry = markProvenance(append(systemFacts, namespaceFacts()...))

// markProvenance leads every fact's catalog description with its value-source
// class, in both languages.
func markProvenance(facts []Fact) []Fact {
	for i, f := range facts {
		switch f.Provenance {
		case ProvenanceParsed:
			f.Description = Text{"解析值：" + f.Description.ZH, "Parsed: " + f.Description.EN}
		case ProvenanceDerived:
			f.Description = Text{"派生值：" + f.Description.ZH, "Derived: " + f.Description.EN}
		case ProvenanceInvented:
			f.Description = Text{"未声明的自创值：" + f.Description.ZH, "Invented (undeclared): " + f.Description.EN}
		default:
			f.Description = Text{"采集值：" + f.Description.ZH, "Collected: " + f.Description.EN}
		}
		facts[i] = f
	}
	return facts
}

var systemFacts = []Fact{
	// anime
	fact("anime.id", TypeString, PhaseRelease, scenarioAll|scenarioWorkflow, ScenarioNaming|scenarioFile,
		Text{"作品 ID", "Anime ID"}, Text{"订阅作品的标识。", "Identifier of the subscribed anime."},
		"ma_silent_witch_001", func(f *FactSet) any { return f.Anime.ID }),
	fact("anime.title", TypeString, PhaseRelease, scenarioAll|scenarioWorkflow, ScenarioNaming|scenarioFile,
		Text{"标题", "Title"}, Text{"有效标题：自定义、来源标题、TMDB 中文、BGM 中文、BGM、基础标题，取第一个有值的。始终有定义；基础标题必填，所以实际不为空。原样输出，不做清理。", "Effective title: custom, provider title, TMDB Chinese, BGM Chinese, BGM, then the base title, whichever comes first. Always defined; the base title is required, so it is never empty in practice. Output as stored, not cleaned."},
		sampleTitle, func(f *FactSet) any { return f.Anime.Title }).derived(),
	fact("anime.title_source", TypeString, PhaseRelease, ScenarioNaming, ScenarioNaming,
		Text{"标题来源", "Title source"}, Text{"有效标题取自哪里：override、provider、tmdb_cn、bgm_cn、bgm、base。", "Where the effective title came from: override, provider, tmdb_cn, bgm_cn, bgm or base."},
		TitleSourceOverride, func(f *FactSet) any { return f.Anime.TitleSource }).derived(),
	fact("anime.title_override", TypeString, PhaseRelease, ScenarioNaming|ScenarioDownloadSubmitted, ScenarioNaming|ScenarioDownloadSubmitted,
		Text{"自定义标题", "Custom title"}, Text{"为作品设置的标题，覆盖整条标题链。", "Title set for the anime; it overrides the whole title chain."},
		"巨人", func(f *FactSet) any { return f.Anime.TitleOverride }),
	fact("anime.provider_title", TypeString, PhaseRelease, ScenarioNaming, ScenarioNaming,
		Text{"来源标题", "Provider title"}, Text{"订阅时来源（AniBT/Mikan）目录提供的作品标题；custom/oneshot 或目录无该条目时为空。", "The work title the subscription source (AniBT/Mikan) catalog provided at subscribe time; empty for custom/oneshot or an entry the catalog does not carry."},
		"进击的巨人", func(f *FactSet) any { return f.Anime.ProviderTitle }),
	fact("anime.title_base", TypeString, PhaseRelease, ScenarioNaming, ScenarioNaming,
		Text{"基础标题", "Base title"}, Text{"作品记录上保存的标题，标题顺序的最后兜底。", "The title stored on the anime record, the last fallback of the title order."},
		sampleTitle, func(f *FactSet) any { return f.Anime.TitleBase }).hidden(),
	fact("anime.kind", TypeString, PhaseRelease, ScenarioNaming, ScenarioNaming,
		Text{"类型", "Kind"}, Text{"tv 或 movie，决定使用剧集还是电影模板。TMDB 绑定为电影或文件识别为电影时为 movie，否则为 tv；始终有值。", "tv or movie; selects the TV or movie template. movie when the TMDB binding or the file is a movie, otherwise tv; never empty."},
		"tv", func(f *FactSet) any { return f.Anime.Kind }).derived(),
	fact("anime.season", TypeInteger, PhaseRelease, ScenarioNaming, ScenarioNaming,
		Text{"季", "Season"}, Text{"在订阅中填写的季号；0 为特别篇。未填写时为空，不会默认为 1，也不读取 TMDB 绑定的季。匹配 TMDB 时，若季号尚未填写会一并填入。", "The season set on the subscription; 0 is specials. Empty when not set: it never defaults to 1 and never reads the TMDB binding's season. Matching TMDB fills it in when it is not set yet."},
		int64(1), func(f *FactSet) any { return f.Anime.Season }),
	fact("anime.season_folder", TypeString, PhaseRelease, ScenarioNaming, ScenarioNaming,
		Text{"季目录", "Season folder"}, Text{"季目录名，如 Season 01；第 0 季为 Season 00。未填写季或电影时为空。默认模板把这一目录作为季的一级目录，未填写季的剧集会因它为空而命名失败；修复是填写季。", "Season folder name, such as Season 01; season 0 reads Season 00. Empty when the season is not set or for a movie. The default template keeps it as the season's folder level, so a TV episode with no season fails naming on it; the repair is setting the season."},
		"Season 01", func(f *FactSet) any { return f.Anime.SeasonFolder }).derived(),
	fact("anime.season_path", TypeString, PhaseRelease, ScenarioNaming, ScenarioNaming,
		Text{"季目录（含斜杠）", "Season folder path"}, Text{"带前导斜杠的季目录，如 /Season 01；第 0 季（特别篇）为 /Season 00。未填写季或电影时为空，接在作品目录后面也不会留下空目录。", "The season folder with its leading slash, such as /Season 01; season 0 (specials) reads /Season 00. Empty when the season is not set or for a movie, so appended to the work folder it never leaves an empty folder level."},
		"/Season 01", func(f *FactSet) any { return f.Anime.SeasonPath }).derived(),
	fact("anime.year", TypeString, PhaseRelease, ScenarioNaming, ScenarioNaming,
		Text{"年份", "Year"}, Text{"在订阅中填写的年份（1900–2100）；未填写时为空，不读取元数据年份。匹配 TMDB 时可一键填入。", "The year set on the subscription (1900–2100); empty when not set, never read from metadata. Matching TMDB can fill it in."},
		sampleYear, func(f *FactSet) any { return f.Anime.Year }),
	fact("anime.group", TypeString, PhaseRelease, scenarioAll|scenarioWorkflow, scenarioAll|scenarioWorkflow,
		Text{"字幕组", "Group"}, Text{"订阅来源配置的字幕组显示名（来源目录的组、或自定义来源填写的组）；来源没有配置组名时为空。不含标题前缀组，也不回退到任何其他值。", "The fansub group display name the subscription source recorded (a provider catalog group, or the group filled in for a custom source); empty when the source configures none. Not the release title's group and no fallback of any kind."},
		"示例字幕组", func(f *FactSet) any { return f.Anime.Group }),

	// metadata bindings
	fact("bgm.id", TypeString, PhaseRelease, scenarioAll, scenarioAll,
		Text{"BGM ID", "BGM ID"}, Text{"绑定的 Bangumi 条目 ID。", "Bound Bangumi subject ID."},
		"425998", func(f *FactSet) any { return f.Bgm.ID }),
	fact("bgm.title", TypeString, PhaseRelease, scenarioAll, scenarioAll,
		Text{"BGM 标题", "BGM title"}, Text{"绑定条目在 Bangumi 的原名，接受发布时冻结。", "Original name of the bound Bangumi subject, frozen at accept."},
		"進撃の巨人", func(f *FactSet) any { return f.Bgm.Title }),
	fact("bgm.title_cn", TypeString, PhaseRelease, scenarioAll, scenarioAll,
		Text{"BGM 中文标题", "BGM Chinese title"}, Text{"绑定条目在 Bangumi 的中文名，接受发布时冻结。", "Chinese name of the bound Bangumi subject, frozen at accept."},
		"进击的巨人", func(f *FactSet) any { return f.Bgm.TitleCN }),
	fact("tmdb.id", TypeString, PhaseRelease, scenarioAll, scenarioAll,
		Text{"TMDB ID", "TMDB ID"}, Text{"绑定的 TMDB 作品 ID。", "Bound TMDB work ID."},
		"259972", func(f *FactSet) any { return f.Tmdb.ID }),
	fact("tmdb.title_cn", TypeString, PhaseRelease, scenarioAll, scenarioAll,
		Text{"TMDB 中文标题", "TMDB Chinese title"}, Text{"绑定作品在 TMDB 按请求语言（language=zh-CN）返回的标题，接受发布时冻结。", "The title TMDB returned for the request language (language=zh-CN) of the bound work, frozen at accept."},
		"进击的巨人", func(f *FactSet) any { return f.Tmdb.TitleCN }),
	fact("tmdb.original_title", TypeString, PhaseRelease, scenarioAll, scenarioAll,
		Text{"TMDB 原标题", "TMDB original title"}, Text{"绑定作品在 TMDB 的原始标题，接受发布时冻结。", "Original title of the bound TMDB work, frozen at accept."},
		"進撃の巨人", func(f *FactSet) any { return f.Tmdb.OriginalTitle }),
	// tmdb.summary is notification-scope only: a multi-paragraph overview in a
	// file path is structurally wrong, so the naming scenario lists no row.
	fact("tmdb.summary", TypeString, PhaseRelease, scenarioMedia, scenarioMedia,
		Text{"TMDB 简介", "TMDB summary"}, Text{"绑定作品在 TMDB 的作品简介，匹配时记录、接受发布时冻结；通知发送时读取冻结值。未绑定 TMDB 或从未观察到简介时为空；之后匹配刷新记录只影响后续接受。", "The TMDB overview of the bound work, recorded at match time and frozen at accept; notifications read the frozen value. Empty when TMDB is not bound or the overview was never observed; a later match refreshes the record and only subsequent acceptances freeze the new value."},
		"高新技术被广泛应用的未来世界，人工智能与人性的边界逐渐模糊。", func(f *FactSet) any { return f.Tmdb.Summary }),
	fact("tmdb.kind", TypeString, PhaseRelease, scenarioAll, scenarioAll,
		Text{"TMDB 类型", "TMDB kind"}, Text{"tv 或 movie。", "tv or movie."},
		"tv", func(f *FactSet) any { return f.Tmdb.Kind }),
	fact("tmdb.season", TypeInteger, PhaseRelease, scenarioAll, scenarioAll,
		Text{"TMDB 季", "TMDB season"}, Text{"绑定的 TMDB 季号，用于查询 TMDB 剧集信息；命名的季见 anime.season。", "Bound TMDB season number, used to look up TMDB episodes; the named season is anime.season."},
		int64(1), func(f *FactSet) any { return f.Tmdb.Season }),
	fact("bgm.ep_title", TypeString, PhaseExecution, ScenarioNaming|scenarioFile, ScenarioNaming|scenarioFile,
		Text{"BGM 集标题", "BGM episode title"}, Text{"查询 Bangumi 条目分集得到的本集标题，查询结果会缓存。未绑定 Bangumi、没有集数、该集没有标题或查询尚未成功时为空；重命名执行时才查询，下载前的预览显示为占位。", "The episode title of the bound Bangumi subject, queried and cached; empty when Bangumi is not bound, there is no episode number, the provider has no title for that episode, or the lookup has not succeeded yet. Queried at the rename execution only; previews before the download show a placeholder."},
		"炎の七夕 祭り", func(f *FactSet) any { return f.Bgm.EpTitle }),
	fact("tmdb.ep_title", TypeString, PhaseExecution, ScenarioNaming|scenarioFile, ScenarioNaming|scenarioFile,
		Text{"TMDB 集标题", "TMDB episode title"}, Text{"查询绑定 TMDB 作品分集得到的本集标题（按请求语言 zh-CN），查询结果会缓存。未绑定 TMDB、没有集数、该集没有标题或查询尚未成功时为空；重命名执行时才查询，下载前的预览显示为占位。", "The episode title of the bound TMDB work in the request language (language=zh-CN), queried and cached; empty when TMDB is not bound, there is no episode number, the provider has no title for that episode, or the lookup has not succeeded yet. Queried at the rename execution only; previews before the download show a placeholder."},
		"炎の七夕 祭り", func(f *FactSet) any { return f.Tmdb.EpTitle }),

	// release
	fact("release.id", TypeString, PhaseRelease, scenarioAll|scenarioRelease, ScenarioNaming|scenarioFile|ScenarioDownloadFailed|ScenarioUnknownImported,
		Text{"发布 ID", "Release ID"}, Text{"发布条目的标识。", "Identifier of the release."},
		"rel_001", func(f *FactSet) any { return f.Release.ID }),
	fact("release.title", TypeString, PhaseRelease, scenarioAll|scenarioRelease, scenarioAll|ScenarioDownloadFailed|ScenarioUnknownImported,
		Text{"发布标题", "Release title"}, Text{"RSS 中的原始标题。", "Original title from the feed."},
		sampleRelease, func(f *FactSet) any { return f.Release.Title }),
	fact("release.published_at", TypeTimestamp, PhaseRelease, ScenarioDownloadSubmitted|ScenarioReleaseDiscovered, ScenarioDownloadSubmitted|ScenarioReleaseDiscovered,
		Text{"发布时间", "Published at"}, Text{"发布时间，RFC 3339。", "Publication time, RFC 3339."},
		"2026-07-01T12:00:00Z", func(f *FactSet) any { return f.Release.PublishedAt }),
	fact("release.episodes", TypeString, PhaseRelease, ScenarioDownloadSubmitted|ScenarioReleaseDiscovered, ScenarioDownloadSubmitted|ScenarioReleaseDiscovered,
		Text{"下载集数", "Episodes"}, Text{"这次下载要入库的集数，如 01-04 或 01, 03, 07；合集时单集字段为空。", "Episodes this download will place, such as 01-04 or 01, 03, 07; for a batch the single-episode facts are empty."},
		"01-04", func(f *FactSet) any { return f.Release.Episodes }).derived(),
	fact("release.src", TypeString, PhaseRelease, scenarioMedia|ScenarioReleaseDiscovered|ScenarioDownloadFailed, scenarioMedia|ScenarioReleaseDiscovered|ScenarioDownloadFailed,
		Text{"来源", "Source"}, Text{"发布所在的来源：媒体事件为 mikan 或 anibt；发布工作流事件为 provider_group 或 custom_rss；载荷未记录时为空。", "Where the release came from: mikan or anibt for a media event, provider_group or custom_rss for a release workflow event; empty when the payload records none."},
		"anibt", func(f *FactSet) any { return f.Release.Src }),
	fact("release.subtitle_mode", TypeString, PhaseRelease, ScenarioNaming, 0,
		Text{"字幕方式", "Subtitle mode"}, Text{"embedded、external、mixed 或 unknown，由发布标题解析的字幕方式标志 rt.sub_flag 归并：仅 soft 或 hard 为 embedded，仅 ex 为 external，ex 与 soft/hard 并存为 mixed，都没有时为 unknown。", "embedded, external, mixed or unknown, folded from the title-parse subtitle flags rt.sub_flag: soft or hard alone embedded, ex alone external, ex beside soft/hard mixed, none unknown."},
		"embedded", func(f *FactSet) any { return f.Release.SubtitleMode }).derived(),
	fact("release.lang", TypeString, PhaseRelease, ScenarioNaming, ScenarioNaming,
		Text{"语言", "Language"}, Text{"从标题识别的主语言代码，如 sc、tc、jpsc。", "Main language code read from the title, such as sc, tc or jpsc."},
		"sc", func(f *FactSet) any { return f.Release.Lang }).parsed(),
	fact("release.langs", TypeList, PhaseRelease, ScenarioNaming, ScenarioNaming,
		Text{"语言集合", "Languages"}, Text{"识别到的全部语言代码。", "Every language code read from the title."},
		[]string{"sc", "tc"}, func(f *FactSet) any { return f.Release.Langs }).parsed(),
	fact("release.lang_tag", TypeList, PhaseRelease, ScenarioNaming, ScenarioNaming,
		Text{"语言标签", "Language tags"}, Text{"release.langs 里每个语言代码对应的 BCP 47 标签，如 zh-Hans、zh-Hant、ja；没有对应标签的代码不列出。由 release.langs 转换得到，不是另一路解析结果。", "The BCP 47 tag of each release.langs code, such as zh-Hans, zh-Hant or ja; a code with no tag is left out. Converted from release.langs, not a second parse."},
		[]string{"zh-Hans", "zh-Hant"}, func(f *FactSet) any { return f.Release.LangTag }).derived(),

	// subtitle language: file first, then release (like the episode)
	fact("sub.lang", TypeString, PhaseRelease, ScenarioNaming, ScenarioNaming,
		Text{"字幕语言", "Subtitle language"}, Text{"字幕文件的语言代码，如 SC、TC、JPSC；多个用 + 连接。先读字幕文件名自己的标记，读不出时取发布标题的 rt.sub_lang；都没有时为空。仅字幕文件有值。", "Language code of the subtitle file, such as SC, TC or JPSC; several join with +. Read from the subtitle file name's own tag first, else the release title's rt.sub_lang; empty when neither has one. Subtitle files only."},
		"SC", func(f *FactSet) any { return f.Sub.Lang }).derived(),
	fact("sub.langs", TypeList, PhaseRelease, ScenarioNaming, ScenarioNaming,
		Text{"字幕语言集合", "Subtitle languages"}, Text{"sub.lang 的全部语言代码。", "Every language code of sub.lang."},
		[]string{"SC"}, func(f *FactSet) any { return f.Sub.Langs }).derived(),
	fact("sub.lang_tag", TypeString, PhaseRelease, ScenarioNaming, ScenarioNaming,
		Text{"字幕语言标签", "Subtitle language tag"}, Text{"sub.lang 对应的一个 BCP 47 标签（媒体服务器每个字幕只认一种语言）。含中文时：只有简体为 zh-Hans，只有繁体为 zh-Hant，简繁都有为 zh；不含中文时取第一种语言，如 ja、en；读不出时为空。", "One BCP 47 tag for sub.lang (a media server reads one language per subtitle). With Chinese: simplified only is zh-Hans, traditional only zh-Hant, both zh. Without Chinese: the first language, such as ja or en. Empty when none is known."},
		"zh-Hans", func(f *FactSet) any { return f.Sub.LangTag }).derived(),
	fact("sub.lang_source", TypeString, PhaseRelease, ScenarioNaming, ScenarioNaming,
		Text{"字幕语言来源", "Subtitle language source"}, Text{"sub.lang 取自哪里：file 为字幕文件名，release 为发布标题。", "Where sub.lang came from: file means the subtitle file name, release the release title."},
		"file", func(f *FactSet) any { return f.Sub.LangSource }).derived(),
	fact("sub.lang_code", TypeString, PhaseRelease, ScenarioNaming, ScenarioNaming,
		Text{"字幕语言短码", "Subtitle language code"}, Text{"sub.lang 的短码（小写，jpsc 一类的组合码）：jpsc 为简体+日文并胜过 jptc，jptc 为繁体+日文；单一语言取它自己的码，多个语言按 jpsc > jptc > sc > tc > eng > jpn 优先取，其他组合为 other。", "Lowercase short code of sub.lang (the jpsc-style composites): jpsc for simplified+japanese and beats jptc, jptc traditional+japanese; a single language keeps its own code, several pick by the priority jpsc > jptc > sc > tc > eng > jpn, everything else is other."},
		"jpsc", func(f *FactSet) any { return f.Sub.LangCode }).derived(),
	fact("sub.lang_name", TypeString, PhaseRelease, ScenarioNaming, ScenarioNaming,
		Text{"字幕语言显示", "Subtitle language name"}, Text{"sub.lang 按整组语言列表组成的显示词（简体、繁体、简日、繁日、简繁、简繁日、简英、繁英），非中文列表显示单一语言的最高优先词（英文、日文、韩文）。中文显示追加 rt.sub_flag 的方式词（内封、内嵌、外挂、混合）；没有明确方式时组合显示补双语（AniBT INTERNAL 为内封），单个简体/繁体不带。", "Display word composed from the whole sub.lang list (Simplified, Traditional, Simplified+Japanese, Traditional+Japanese, Simplified+Traditional, the three-way and the two English combinations); a list without Chinese shows the top word of one language (English, Japanese, Korean). A Chinese display appends the rt.sub_flag mode word (muxed, hardcoded, external, mixed); with no resolved type a composite display takes bilingual (AniBT INTERNAL reads muxed), a single Simplified/Traditional stays bare."},
		"简繁日内封", func(f *FactSet) any { return f.Sub.LangName }).derived(),

	// episode
	fact("ep.number", TypeString, PhaseRelease, ScenarioNaming|ScenarioDownloadSubmitted|ScenarioFileCompleted|ScenarioReleaseDiscovered, ScenarioNaming|ScenarioDownloadSubmitted|ScenarioFileCompleted|ScenarioReleaseDiscovered,
		Text{"集数", "Episode"}, Text{"应用偏移后的集数，如 12，不补零：由解析到的原文（ep.raw）与生效的偏移（ep.offset）算出，偏移只在身份解析时应用一次。有集数识别时才有定义；未识别的文件走未识别模板。", "Episode number after the offset, such as 12, not zero-padded: computed from the parsed capture (ep.raw) and the effective offset (ep.offset), which the identity resolver applies exactly once. Defined only with an episode identity; an unrecognized file uses the unknown template."},
		"7", func(f *FactSet) any { return f.Ep.Number }).derived(),
	fact("ep.number_end", TypeString, PhaseRelease, ScenarioNaming, ScenarioNaming,
		Text{"结束集数", "Last episode"}, Text{"一个文件含多集时的最后一集，如 E01-E02 中的 2；单集为空。", "Last episode of a file holding several, such as 2 in E01-E02; empty for a single episode."},
		"8", func(f *FactSet) any { return f.Ep.NumberEnd }).derived(),
	fact("ep.s00e00", TypeString, PhaseRelease, ScenarioNaming, ScenarioNaming,
		Text{"季集编号", "Season and episode"}, Text{"S01E07；多集文件为 S01E07-E08；第 0 季（特别篇）为 S00E07；集数少于两位时补零。未填写季时只有集数部分，如 E07、E07-E08。没有集数或电影时为空。", "S01E07; S01E07-E08 for a multi-episode file; S00E07 for season 0 (specials). Episodes are zero-padded to two digits. When the season is not set only the episode part, such as E07 or E07-E08. Empty without an episode number or for a movie."},
		"S01E07", func(f *FactSet) any { return f.Ep.S00E00 }).derived(),
	fact("ep.raw", TypeString, PhaseRelease, ScenarioNaming|ScenarioDownloadSubmitted|ScenarioFileCompleted|ScenarioReleaseDiscovered, ScenarioNaming|ScenarioDownloadSubmitted|ScenarioFileCompleted|ScenarioReleaseDiscovered,
		Text{"原始集数", "Raw episode"}, Text{"偏移前匹配到的原文。", "Matched text before the offset."},
		"07", func(f *FactSet) any { return f.Ep.Raw }).parsed(),
	fact("ep.display", TypeString, PhaseRelease, ScenarioNaming|ScenarioDownloadSubmitted|ScenarioFileCompleted|ScenarioReleaseDiscovered, ScenarioNaming|ScenarioDownloadSubmitted|ScenarioFileCompleted|ScenarioReleaseDiscovered,
		Text{"显示集数", "Display episode"}, Text{"用于显示的集数；今天每个投影都把它设为 ep.number，尚无独立的显示文本生产者。", "Episode text for display; every projection today sets it to ep.number, so it has no independent producer yet."},
		"7", func(f *FactSet) any { return f.Ep.Display }).derived(),
	fact("ep.offset", TypeInteger, PhaseRelease, ScenarioNaming|ScenarioDownloadSubmitted|ScenarioFileCompleted, ScenarioDownloadSubmitted|ScenarioFileCompleted,
		Text{"集数偏移", "Episode offset"}, Text{"生效的集数偏移。", "Episode offset in effect."},
		int64(0), func(f *FactSet) any { return f.Ep.Offset }),
	fact("ep.source", TypeString, PhaseRelease, ScenarioNaming, ScenarioNaming,
		Text{"集数来源", "Episode source"}, Text{"集数取自哪份证据：file_parser 为文件解析，release_parser 为发布标题解析，anibt 为来源站点标签，movie 为剧场版，manual 为手动匹配。", "Which evidence supplied the episode: file_parser, release_parser, anibt, movie, or manual for a manual match."},
		"release_parser", func(f *FactSet) any { return f.Ep.Source }).derived(),
	fact("ep.kind", TypeString, PhaseRelease, ScenarioNaming, ScenarioNaming,
		Text{"特别篇类型", "Special kind"}, Text{"正片为空；ncop、nced 或 special。由发布标题里的关键词判定，不是解析器给出的集数身份证据。", "Empty for a regular episode; ncop, nced or special. Decided by a keyword in the release title, not parser episode-identity evidence."},
		"special", func(f *FactSet) any { return f.Ep.Kind }).derived(),

	// torrent
	fact("torrent.name", TypeString, PhaseRelease, ScenarioDownloadSubmitted, ScenarioDownloadSubmitted,
		Text{"种子名称", "Torrent name"}, Text{"下载客户端确认的种子名称。", "Torrent name confirmed by the download client."},
		sampleRelease, func(f *FactSet) any { return f.Torrent.Name }),
	fact("torrent.size", TypeInteger, PhaseRelease, scenarioAll|ScenarioReleaseDiscovered, scenarioAll|ScenarioReleaseDiscovered,
		Text{"种子大小", "Torrent size"}, Text{"种子内全部文件的字节数。", "Total size of the files in the torrent, in bytes."},
		sampleFileSize, func(f *FactSet) any { return f.Torrent.Size }).parsed(),
	fact("torrent.file_count", TypeInteger, PhaseRelease, scenarioAll|ScenarioReleaseDiscovered, scenarioAll|ScenarioReleaseDiscovered,
		Text{"文件数", "File count"}, Text{"种子内的文件数。", "Number of files in the torrent."},
		int64(1), func(f *FactSet) any { return f.Torrent.FileCount }).parsed(),
	fact("torrent.hash_v1", TypeString, PhaseRelease, scenarioAll, scenarioAll,
		Text{"InfoHash v1", "InfoHash v1"}, Text{"BitTorrent v1 info hash。", "BitTorrent v1 info hash."},
		"5d1c5c8e0b7a4f3e9a2d6c1b8e7f4a3d2c1b0a9e", func(f *FactSet) any { return f.Torrent.HashV1 }).parsed(),
	fact("torrent.hash_v2", TypeString, PhaseRelease, scenarioAll, scenarioAll,
		Text{"InfoHash v2", "InfoHash v2"}, Text{"BitTorrent v2 info hash。", "BitTorrent v2 info hash."},
		"8f2e6d4c1a9b7e5d3c2b1a0f9e8d7c6b5a4f3e2d1c0b9a8f7e6d5c4b3a2f1e0d", func(f *FactSet) any { return f.Torrent.HashV2 }).parsed(),
	fact("torrent.tree", TypeString, PhaseRelease, ScenarioDownloadSubmitted|ScenarioReleaseDiscovered, ScenarioDownloadSubmitted|ScenarioReleaseDiscovered,
		Text{"种子结构", "Torrent structure"}, Text{"种子内顶层目录与文件的大小一览，每行一项；顶层目录带递归大小与文件数，末尾一行合计。没有种子载荷时为空。", "One line per top-level folder or file in the torrent; a folder carries its recursive size and file count, and the last line is the total. Empty when there is no torrent payload."},
		"Show S01/   12.4 GiB · 24 files\nreadme.txt  4.0 KiB\n合计: 12.4 GiB · 25 files", func(f *FactSet) any { return f.Torrent.Tree }).derived(),

	// file
	fact("file.name", TypeString, PhaseRelease, ScenarioNaming|scenarioFile, scenarioFile,
		Text{"文件名", "File name"}, Text{"原始完整文件名。", "Original full file name."},
		sampleFile, func(f *FactSet) any { return f.File.Name }),
	fact("file.stem", TypeString, PhaseRelease, ScenarioNaming|scenarioFile, scenarioFile,
		Text{"文件主名", "File stem"}, Text{"不含扩展名的文件名。", "File name without the extension."},
		sampleFileStem, func(f *FactSet) any { return f.File.Stem }),
	fact("file.ext", TypeString, PhaseRelease, ScenarioNaming|scenarioFile, scenarioFile,
		Text{"扩展名", "Extension"}, Text{"小写扩展名，不带点。无扩展名的文件为空。", "Lowercase extension without the dot. Empty for a file without one."},
		"mkv", func(f *FactSet) any { return f.File.Ext }),
	fact("file.size", TypeInteger, PhaseRelease, ScenarioNaming|scenarioFile, scenarioFile,
		Text{"文件大小", "File size"}, Text{"文件字节数。", "File size in bytes."},
		sampleFileSize, func(f *FactSet) any { return f.File.Size }),
	fact("file.kind", TypeString, PhaseRelease, ScenarioNaming|scenarioFile, scenarioFile,
		Text{"文件类型", "File kind"}, Text{"video、subtitle 或 other。", "video, subtitle or other."},
		"video", func(f *FactSet) any { return f.File.Kind }).derived(),
	fact("media.resolution", TypeString, PhaseExecution, ScenarioNaming|scenarioFile, ScenarioNaming|scenarioFile,
		Text{"分辨率", "Resolution"}, Text{"检查文件得到的分辨率，如 1080p；文件未带时取发布标题中的分辨率，都没有时为空。下载前的预览显示为占位。", "Resolution read from the file, such as 1080p; when the file has none, the release title's resolution, else empty. Previews before the download show a placeholder."},
		"1080p", func(f *FactSet) any { return f.File.Resolution }),
	fact("media.video_codec", TypeString, PhaseExecution, ScenarioNaming|scenarioFile, ScenarioNaming|scenarioFile,
		Text{"视频编码", "Video codec"}, Text{"检查文件得到的视频编码，如 HEVC；读不到时为空。下载前的预览显示为占位。", "Video codec read from the file, such as HEVC; empty when it cannot be read. Previews before the download show a placeholder."},
		"HEVC", func(f *FactSet) any { return f.File.VideoCodec }),
	fact("media.audio_codec", TypeString, PhaseExecution, ScenarioNaming|scenarioFile, ScenarioNaming|scenarioFile,
		Text{"音频编码", "Audio codec"}, Text{"检查文件得到的音频编码，如 AAC；读不到时为空。下载前的预览显示为占位。", "Audio codec read from the file, such as AAC; empty when it cannot be read. Previews before the download show a placeholder."},
		"AAC", func(f *FactSet) any { return f.File.AudioCodec }),
	fact("media.container", TypeString, PhaseExecution, ScenarioNaming|scenarioFile, ScenarioNaming|scenarioFile,
		Text{"容器", "Container"}, Text{"检查文件得到的容器格式，如 mkv；读不到时为空。下载前的预览显示为占位。", "Container format read from the file, such as mkv; empty when it cannot be read. Previews before the download show a placeholder."},
		"mkv", func(f *FactSet) any { return f.File.Container }),
	fact("media.hdr", TypeString, PhaseExecution, ScenarioNaming|scenarioFile, ScenarioNaming|scenarioFile,
		Text{"HDR 格式", "HDR"}, Text{"检查文件得到的 HDR 格式，如 hdr10、hlg、dovi；多格式按文件中的顺序逗号连接（hdr10,dovi），SDR 或读不到时为空。下载前的预览显示为占位。", "HDR read from the file, such as hdr10, hlg or dovi; multiple formats join with commas in the file's order (hdr10,dovi); empty for SDR or when it cannot be read. Previews before the download show a placeholder."},
		"hdr10", func(f *FactSet) any { return f.Info.HDR }),
	fact("file.sub.lang", TypeString, PhaseRelease, ScenarioNaming, ScenarioNaming,
		Text{"字幕文件语言标签", "Subtitle file language tag"}, Text{"字幕文件名标记中第一个语言的 BCP 47 标签，如 zh-Hans；仅字幕文件有值。", "BCP 47 tag of the first language in the subtitle file name's tag, such as zh-Hans; subtitle files only."},
		"zh-Hans", func(f *FactSet) any { return f.File.Sub.Lang }).parsed(),
	fact("file.sub.langs", TypeList, PhaseRelease, ScenarioNaming, ScenarioNaming,
		Text{"字幕文件语言标签集合", "Subtitle file language tags"}, Text{"字幕文件名标记中每一段的 BCP 47 标签，读不出的段记为 und。", "BCP 47 tag of each part of the subtitle file name's tag; a part that reads as no language is und."},
		[]string{"zh-Hans", "ja"}, func(f *FactSet) any { return f.File.Sub.Langs }).parsed(),
	fact("file.sub.format", TypeString, PhaseRelease, ScenarioNaming, ScenarioNaming,
		Text{"字幕格式", "Subtitle format"}, Text{"ass、ssa、srt 或 vtt。", "ass, ssa, srt or vtt."},
		"ass", func(f *FactSet) any { return f.File.Sub.Format }).parsed(),
	fact("file.sub.suffix", TypeString, PhaseRelease, ScenarioNaming, ScenarioNaming,
		Text{"字幕后缀", "Subtitle suffix"}, Text{"主名和扩展名之间的原始标记，原样不变，如 chs&jpn。", "Original tag between the stem and the extension, verbatim, such as chs&jpn."},
		"chs_forced", func(f *FactSet) any { return f.File.Sub.Suffix }).parsed(),
	fact("file.sub.forced", TypeBoolean, PhaseRelease, ScenarioNaming, ScenarioNaming,
		Text{"强制字幕", "Forced"}, Text{"后缀带 forced 标记。", "The suffix carries a forced marker."},
		true, func(f *FactSet) any { return f.File.Sub.Forced }).parsed(),
	fact("file.sub.sdh", TypeBoolean, PhaseRelease, ScenarioNaming, ScenarioNaming,
		Text{"听障字幕", "SDH"}, Text{"后缀带 SDH 标记。", "The suffix carries an SDH marker."},
		false, func(f *FactSet) any { return f.File.Sub.SDH }).parsed(),
	fact("file.sub.commentary", TypeBoolean, PhaseRelease, ScenarioNaming, ScenarioNaming,
		Text{"评论字幕", "Commentary"}, Text{"后缀带评论标记。", "The suffix carries a commentary marker."},
		false, func(f *FactSet) any { return f.File.Sub.Commentary }).parsed(),
	fact("file.sub.default", TypeBoolean, PhaseRelease, ScenarioNaming, ScenarioNaming,
		Text{"默认字幕", "Default"}, Text{"后缀带 default 标记。", "The suffix carries a default marker."},
		false, func(f *FactSet) any { return f.File.Sub.Default }).parsed(),

	// library target
	fact("out.dir", TypeString, PhaseExecution, ScenarioFileCompleted, ScenarioFileCompleted,
		Text{"目标目录", "Target folder"}, Text{"相对于入库位置根的目录。", "Folder relative to the location's root."},
		sampleOutDir, func(f *FactSet) any { return f.Out.Dir }).derived(),
	fact("out.name", TypeString, PhaseExecution, ScenarioFileCompleted, ScenarioFileCompleted,
		Text{"目标文件名", "Target name"}, Text{"入库后的文件名。", "File name in the library."},
		sampleOutName, func(f *FactSet) any { return f.Out.Name }).derived(),
	fact("out.path", TypeString, PhaseExecution, ScenarioFileCompleted, ScenarioFileCompleted,
		Text{"目标路径", "Target path"}, Text{"相对于入库位置根的完整路径。", "Full path relative to the location's root."},
		sampleOutDir+"/"+sampleOutName, func(f *FactSet) any { return f.Out.Path }).derived(),
	fact("op.kind", TypeString, PhaseExecution, scenarioFile, scenarioFile,
		Text{"入库方式", "Operation"}, Text{"copy、move 或 hardlink。", "copy, move or hardlink."},
		"move", func(f *FactSet) any { return f.Op.Kind }),
	fact("op.conflict", TypeString, PhaseExecution, scenarioFile, scenarioFile,
		Text{"冲突策略", "Conflict policy"}, Text{"目标已存在时的处理方式。", "What happens when the target already exists."},
		"versioned", func(f *FactSet) any { return f.Op.Conflict }),

	// notification only
	fact("event.type", TypeString, PhaseExecution, scenarioNotify, 0,
		Text{"事件", "Event"}, Text{"触发通知的事件。", "Event that triggered the notification."},
		"download_submitted", func(f *FactSet) any { return f.Event.Type }).derived().
		with(ScenarioFileCompleted, "file_operation_completed").with(ScenarioFileFailed, "file_operation_failed").
		with(ScenarioReleaseDiscovered, "release_discovered").with(ScenarioDownloadFailed, "download_failed").
		with(ScenarioSubscriptionChanged, "subscription_changed").with(ScenarioUnknownImported, "unknown_files_imported"),
	fact("event.at", TypeTimestamp, PhaseExecution, scenarioNotify, 0,
		Text{"发生时间", "Occurred at"}, Text{"事件发生时间，RFC 3339。", "When the event happened, RFC 3339."},
		sampleOccurredAt, func(f *FactSet) any { return f.Event.At }).derived(),
	fact("event.action", TypeString, PhaseExecution, ScenarioSubscriptionChanged, 0,
		Text{"订阅变化", "Subscription change"}, Text{"started、stopped、resumed 或 retired。", "started, stopped, resumed or retired."},
		"started", func(f *FactSet) any { return f.Event.Action }).derived(),
	fact("event.link", TypeString, PhaseExecution, ScenarioUnknownImported, ScenarioUnknownImported,
		Text{"应用内链接", "In-app link"}, Text{"应用内的 # 锚点：打开这次获取的执行记录，其中列出需要手动匹配的文件。", "In-app # anchor: opens this acquisition's execution record, which lists the files awaiting a manual match."},
		"#/?record=acq_001", func(f *FactSet) any { return f.Event.Link }).derived(),
	fact("result.outcome", TypeString, PhaseExecution, scenarioMedia, 0,
		Text{"结果", "Outcome"}, Text{"success 或 failure。", "success or failure."},
		"success", func(f *FactSet) any { return f.Result.Outcome }).derived().
		with(ScenarioFileFailed, "failure"),
	fact("result.reason", TypeString, PhaseExecution, scenarioFile, 0,
		Text{"原因", "Reason"}, Text{"结果原因，已去除敏感信息。", "Reason for the outcome, with sensitive details removed."},
		"file_operation_completed", func(f *FactSet) any { return f.Result.Reason }).derived().
		with(ScenarioFileFailed, "目标文件已存在"),
	fact("result.category", TypeString, PhaseExecution, ScenarioDownloadFailed, 0,
		Text{"失败类别", "Failure category"}, Text{"stalled（停滞）、timeout（超时）、client（下载客户端报错）、unobservable（无法读取状态）、occupied（下载器中已有同一种子的任务）或 submit（提交失败）。", "stalled, timeout, client (the download client reported an error), unobservable (status could not be read), occupied (the download client already has a task for this torrent) or submit (submission failed)."},
		"stalled", func(f *FactSet) any { return f.Result.Category }).derived(),
	fact("result.retryable", TypeBoolean, PhaseExecution, ScenarioDownloadFailed, 0,
		Text{"可重试", "Retryable"}, Text{"能否在 RSS 页面重试这个发布。", "Whether the release can be retried from the RSS page."},
		true, func(f *FactSet) any { return f.Result.Retryable }).derived(),
	fact("out.count", TypeInteger, PhaseExecution, ScenarioUnknownImported, 0,
		Text{"文件数", "File count"}, Text{"写入媒体库的未识别文件数。", "Number of unrecognized files written to the library."},
		int64(2), func(f *FactSet) any { return f.Out.Count }).derived(),
	fact("out.files", TypeList, PhaseExecution, ScenarioUnknownImported, 0,
		Text{"文件列表", "Files"}, Text{"写入媒体库的未识别文件：相对于该作品树顶的路径（path）和字节数（size）。", "Unrecognized files written to the library: path relative to the work's tree top (path) and size in bytes (size)."},
		[]map[string]any{{"path": sampleOutDir + "/" + sampleFile, "size": sampleFileSize}}, func(f *FactSet) any { return f.Out.Files }).derived(),
	fact("download.client", TypeString, PhaseExecution, ScenarioDownloadSubmitted, ScenarioDownloadSubmitted,
		Text{"下载客户端", "Download client"}, Text{"任务所在下载客户端的名称。", "Name of the download client holding the task."},
		"家里的 qBittorrent", func(f *FactSet) any { return f.Download.Client }),
	fact("download.task", TypeString, PhaseExecution, ScenarioDownloadSubmitted, ScenarioDownloadSubmitted,
		Text{"下载任务", "Download task"}, Text{"下载客户端确认的任务名称。", "Task name confirmed by the download client."},
		sampleRelease, func(f *FactSet) any { return f.Download.Task }),
}

// namespaceFacts registers the rt and anibt value namespaces, and the source
// group identity namespaces, from the templatefacts field lists. Every field is
// a nullable string (boolean rt fields: a nullable boolean): rt and anibt are nullable in every scenario that concerns
// a release, and the source group identity fields are nullable in naming (the
// scenario that reads them today). A reltitle field's value is the parser's
// output; the tag dimensions and the group identity are stored values.
func namespaceFacts() []Fact {
	facts := []Fact{}
	allRelease := scenarioAll | scenarioRelease
	for _, namespace := range []struct {
		root      string
		label     Text
		fields    []Field
		values    func(*FactSet) Values
		scenarios Scenario
		parsed    bool
	}{
		{RootRt, Text{"reltitle", "reltitle"}, ReltitleFields, func(f *FactSet) Values { return f.Rt }, allRelease, true},
		{RootAnibt, Text{"AniBT", "AniBT"}, AniBTTagFields, func(f *FactSet) Values { return f.Anibt }, allRelease, false},
		// The source group identity is the subscription source's own identity,
		// not a tag value: it shares the anibt root with the AniBT tag fields
		// but each root's fields read null when the source kind does not match.
		{RootAnibt, Text{"AniBT", "AniBT"}, AniBTSourceGroupFields, func(f *FactSet) Values { return f.Anibt }, ScenarioNaming, false},
		{RootMikan, Text{"Mikan", "Mikan"}, MikanSourceGroupFields, func(f *FactSet) Values { return f.Mikan }, ScenarioNaming, false},
	} {
		for _, field := range namespace.fields {
			field, values, scenarios := field, namespace.values, namespace.scenarios
			kind, sample := TypeString, any(field.SampleValue)
			get := func(f *FactSet) any { return values(f)[field.Key] }
			if field.Type == TypeBoolean {
				// A yes/no field is stored as "true"/"false"; absent is null.
				kind, sample = TypeBoolean, field.SampleValue == "true"
				get = func(f *FactSet) any {
					switch values(f)[field.Key] {
					case "true":
						return true
					case "false":
						return false
					}
					return nil
				}
			}
			r := fact(namespace.root+"."+field.Name, kind, PhaseRelease, scenarios, scenarios,
				Text{namespace.label.ZH + " " + field.Label.ZH, namespace.label.EN + " " + field.Label.EN}, field.Text,
				sample, get)
			if namespace.parsed {
				r = r.parsed()
			}
			facts = append(facts, r)
		}
	}
	return facts
}
