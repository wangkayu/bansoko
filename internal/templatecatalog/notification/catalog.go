// Package notification is the single bansoko-template-1 notification core: the
// closed per-trigger variable catalog, the function allowlist, the system
// default templates, the fixed redacted sample facts, and the renderer.
//
// The catalog is the unified fact vocabulary (the templatefacts registry).
// Delivery payloads store facts under their registry paths (see
// templatefacts.NotificationPayloadKeys); a render reads them as-is.
// Template preview, test delivery and business delivery all render through
// RenderMessage, so a template that previews valid renders the same way when
// it is sent. The package is pure: no database, secret, network or clock.
package notification

import (
	"bansoko/internal/templatefacts"
)

const (
	LanguageVersion = "bansoko-template-1"
	// CatalogVersion is the notification template catalog version.
	CatalogVersion = "notification-catalog-v1"

	TriggerDownloadSubmitted         = "download_submitted"
	TriggerFileOperationCompleted    = "file_operation_completed"
	TriggerFileOperationFailed       = "file_operation_failed"
	TriggerReleaseDiscovered         = "release_discovered"
	TriggerDownloadFailed            = "download_failed"
	TriggerSubscriptionChanged       = "subscription_changed"
	TriggerUnknownFilesImported      = "unknown_files_imported"
	TriggerReplacementDeletionFailed = "replacement_deletion_failed"
	// TriggerReplacementApplied fires when a settled replace decision takes
	// over the versions it named; TriggerFileDisplaced fires when one managed
	// file loses its place to another file.
	TriggerReplacementApplied = "replacement_applied"
	TriggerFileDisplaced      = "file_displaced"

	// LocaleZH and LocaleEN select the language of the system default
	// templates.
	LocaleZH = "zh-CN"
	LocaleEN = "en"
)

// Triggers lists the closed notification trigger registry in catalog order.
var Triggers = []string{
	TriggerDownloadSubmitted, TriggerFileOperationCompleted, TriggerFileOperationFailed,
	TriggerReleaseDiscovered, TriggerDownloadFailed, TriggerSubscriptionChanged, TriggerUnknownFilesImported,
	TriggerReplacementDeletionFailed, TriggerReplacementApplied, TriggerFileDisplaced,
}

// VariableType is the catalog value type of one variable.
type VariableType string

const (
	TypeString    VariableType = "string"
	TypeInteger   VariableType = "integer"
	TypeTimestamp VariableType = "timestamp"
	TypeBoolean   VariableType = "boolean"
	TypeList      VariableType = "list"
)

// Variable is one catalog entry. SampleValue is the fixed sample rendering of
// the variable for the trigger the catalog was built for.
type Variable struct {
	Name        string
	Type        VariableType
	Label       string
	Description string
	Source      string
	Nullable    bool
	SampleValue string
	// Phase is release or execution (templatefacts.Phase).
	Phase string
	// LabelKey and DescriptionKey name the generated copy of the variable.
	LabelKey       string
	DescriptionKey string
}

// SupportedCatalog reports whether catalog is the notification catalog
// version the renderer accepts.
func SupportedCatalog(catalog string) bool {
	return catalog == CatalogVersion
}

// Function is one allowlisted template function.
type Function struct {
	Name        string
	Description string
	Arguments   []string
	minArgs     int
	maxArgs     int // -1 = unbounded
}

var functions = []Function{
	{Name: "coalesce", Description: "返回第一个非空且非空字符串的值。", minArgs: 1, maxArgs: -1, Arguments: []string{"value..."}},
	{Name: "defaultIfEmpty", Description: "仅当第一个值为 null 或空字符串时使用回退值。", minArgs: 2, maxArgs: 2, Arguments: []string{"value", "fallback"}},
	{Name: "lower", Description: "将字符串转为小写。", minArgs: 1, maxArgs: 1, Arguments: []string{"value"}},
	{Name: "upper", Description: "将字符串转为大写。", minArgs: 1, maxArgs: 1, Arguments: []string{"value"}},
	{Name: "replace", Description: "替换字符串中所有出现的字面量子串。", minArgs: 3, maxArgs: 3, Arguments: []string{"value", "search", "replacement"}},
	{Name: "if", Description: "根据简单真值条件在两个值之间选择。", minArgs: 3, maxArgs: 3, Arguments: []string{"condition", "then", "else"}},
	{Name: "eq", Description: "比较两个值是否相等，不访问外部资源。", minArgs: 2, maxArgs: 2, Arguments: []string{"left", "right"}},
	{Name: "showIf", Description: "仅当任一参数有值时输出本行，否则整行不渲染。只能写在行首，每行最多一个，不能嵌套。", minArgs: 1, maxArgs: -1, Arguments: []string{"value..."}},
	{Name: "link", Description: "插入一个可点击链接。目标必须是链接变量或安全 URL，标签必须是文字字面量。", minArgs: 2, maxArgs: 2, Arguments: []string{"target", "label"}},
	{Name: "image", Description: "插入指定来源的图片附件，也接受不带凭据的自定义 HTTPS 图片地址。", minArgs: 1, maxArgs: 1, Arguments: []string{"target"}},
}

// Functions returns the closed function allowlist.
func Functions() []Function {
	return append([]Function(nil), functions...)
}

func lookupFunction(name string) (Function, bool) {
	for _, def := range functions {
		if def.Name == name {
			return def, true
		}
	}
	return Function{}, false
}

const (
	onDownload = 1 << iota
	onCompleted
	onFailed

	onFile = onCompleted | onFailed
	onAll  = onDownload | onFile
)

type payloadKeyDef struct {
	name     string
	triggers int
}

// payloadKeyDefs lists the payload keys the producer of each media trigger
// writes. A payload key is the fact's registry path: payloads are stored
// under registry paths with no translation layer.
var payloadKeyDefs = []payloadKeyDef{
	{"anime.id", onAll},
	{"anime.title", onAll},
	{"anime.title_override", onDownload},
	{"release.src", onAll},
	{"anime.group", onAll},
	{"release.id", onAll},
	{"release.title", onAll},
	{"release.published_at", onDownload},
	{"ep.raw", onDownload | onCompleted},
	{"ep.offset", onDownload | onCompleted},
	{"ep.number", onDownload | onCompleted},
	{"ep.display", onDownload | onCompleted},
	{"bgm.id", onAll},
	{"tmdb.id", onAll},
	{"tmdb.kind", onAll},
	{"tmdb.season", onAll},
	{"torrent.name", onDownload},
	{"torrent.tree", onDownload},
	{"torrent.hash_v1", onAll},
	{"torrent.hash_v2", onAll},
	{"event.type", onAll},
	{"event.at", onAll},
	{"result.outcome", onAll},
	{"result.reason", onFile},
	{"download.client", onDownload},
	{"download.task", onDownload},
	{"file.name", onFile},
	{"file.stem", onFile},
	{"file.ext", onFile},
	{"file.size", onFile},
	{"file.kind", onFile},
	{"media.container", onFile},
	{"media.resolution", onFile},
	{"media.video_codec", onFile},
	{"media.audio_codec", onFile},
	{"media.hdr", onFile},
	{"out.dir", onCompleted},
	{"out.name", onCompleted},
	{"out.path", onCompleted},
	{"out.subtitles", onCompleted},
	{"op.kind", onFile},
	{"op.conflict", onFile},
}

func triggerBit(trigger string) (int, bool) {
	switch trigger {
	case TriggerDownloadSubmitted:
		return onDownload, true
	case TriggerFileOperationCompleted:
		return onCompleted, true
	case TriggerFileOperationFailed:
		return onFailed, true
	default:
		return 0, false
	}
}

// PayloadKeys returns the payload keys the producer of a media-pipeline
// trigger writes; each key is a registry fact path. The workflow triggers
// and unknown triggers return nil.
func PayloadKeys(trigger string) []string {
	bit, ok := triggerBit(trigger)
	if !ok {
		return nil
	}
	var keys []string
	for _, def := range payloadKeyDefs {
		if def.triggers&bit != 0 {
			keys = append(keys, def.name)
		}
	}
	return keys
}

// Variables returns the closed variable catalog of a trigger: every visible
// registry fact of the trigger's scenario, each with its sample rendering. ok
// reports whether the trigger is known.
func Variables(trigger string) ([]Variable, bool) {
	scenario, ok := ScenarioOf(trigger)
	if !ok {
		return nil, false
	}
	facts := templatefacts.ScenarioFacts(scenario)
	variables := make([]Variable, 0, len(facts))
	for _, fact := range facts {
		if fact.Hidden {
			continue
		}
		variables = append(variables, Variable{
			Name: fact.Path, Type: VariableType(fact.Type), Label: fact.Label.ZH, Description: fact.Description.ZH,
			Source: fact.Root(), Nullable: fact.NullableIn(scenario), SampleValue: FormatFact(sampleFact(fact.SampleFor(scenario))),
			Phase: string(fact.Phase), LabelKey: fact.LabelKey(), DescriptionKey: fact.DescriptionKey(),
		})
	}
	variables = append(variables,
		Variable{
			Name: ImageVariable, Type: TypeString, Label: "节目图片",
			Description: "插入一张图片附件。服务端依次选择供应商封面、已绑定的 Bangumi／TMDB 海报和内置占位图。",
			Source:      "system", Nullable: false,
			LabelKey: "templateFacts.semantic.image.label", DescriptionKey: "templateFacts.semantic.image.description",
		},
	)
	available := make(map[string]bool, len(variables))
	for _, variable := range variables {
		available[variable.Name] = true
	}
	for _, variable := range derivedLinkVariables(available) {
		variables = append(variables, variable)
	}
	for _, variable := range derivedImageVariables(available) {
		variables = append(variables, variable)
	}
	return variables, true
}

func derivedLinkVariables(available map[string]bool) []Variable {
	defs := []struct {
		name, requires, label, description string
	}{
		{BGMLinkVariable, "bgm.id", "Bangumi 链接", "已绑定 Bangumi 条目的安全页面地址。"},
		{TMDBLinkVariable, "tmdb.id", "TMDB 链接", "已绑定 TMDB 条目的安全页面地址。"},
		{AniBTLinkVariable, "release.src", "AniBT 发布链接", "当前 AniBT 发布的安全页面地址；非 AniBT 发布为空。"},
		{MikanLinkVariable, "release.src", "Mikan 发布链接", "当前 Mikan 发布的安全页面地址；非 Mikan 发布为空。"},
		{DownloadLinkVariable, "release.id", "下载链接", "不含 passkey、token 或其他凭据的下载地址；无法安全外发时为空。"},
	}
	variables := make([]Variable, 0, len(defs))
	for _, def := range defs {
		if !available[def.requires] {
			continue
		}
		variables = append(variables, Variable{Name: def.name, Type: TypeString, Label: def.label, Description: def.description,
			Source: "derived", Nullable: true, SampleValue: "https://example.com/",
			LabelKey:       "templateFacts.semantic." + def.name + ".label",
			DescriptionKey: "templateFacts.semantic." + def.name + ".description"})
	}
	return variables
}

func derivedImageVariables(available map[string]bool) []Variable {
	defs := []struct {
		name, requires, label, description string
	}{
		{BGMImageVariable, "bgm.id", "Bangumi 图片", "指定使用已绑定 Bangumi 条目的海报；不可用时使用内置占位图。"},
		{TMDBImageVariable, "tmdb.id", "TMDB 图片", "指定使用已绑定 TMDB 条目的海报；不可用时使用内置占位图。"},
		{AniBTImageVariable, "release.src", "AniBT 图片", "指定使用当前 AniBT 作品的封面；不可用时使用内置占位图。"},
		{MikanImageVariable, "release.src", "Mikan 图片", "指定使用当前 Mikan 作品的封面；不可用时使用内置占位图。"},
	}
	variables := make([]Variable, 0, len(defs))
	for _, def := range defs {
		if !available[def.requires] {
			continue
		}
		variables = append(variables, Variable{Name: def.name, Type: TypeString, Label: def.label, Description: def.description,
			Source: "derived", Nullable: true,
			LabelKey:       "templateFacts.semantic." + def.name + ".label",
			DescriptionKey: "templateFacts.semantic." + def.name + ".description"})
	}
	return variables
}

// Defaults returns the Chinese system default title/body for a trigger.
func Defaults(trigger string) (title, body string) {
	return DefaultsFor(trigger, LocaleZH)
}

// DefaultsFor returns the system default title/body for a trigger in one
// locale (zh-CN, or en).
func DefaultsFor(trigger, locale string) (title, body string) {
	table := defaultsZH
	if locale == LocaleEN {
		table = defaultsEN
	}
	pair, ok := table[trigger]
	if !ok {
		return "", ""
	}
	variables, _ := Variables(trigger)
	available := make(map[string]bool, len(variables))
	for _, variable := range variables {
		available[variable.Name] = true
	}
	body = "{{image}}\n" + pair[1]
	if available[BGMLinkVariable] {
		body += "\n{{link(bgm.link, 'Bangumi')}}"
	}
	if available[TMDBLinkVariable] {
		body += "\n{{link(tmdb.link, 'TMDB')}}"
	}
	return pair[0], body
}

var defaultsZH = map[string][2]string{
	TriggerDownloadSubmitted: {"已加入下载：{{anime.title}}",
		"{{release.title}}\n字幕组：{{defaultIfEmpty(anime.group, '未知')}}\n集数：{{defaultIfEmpty(ep.display, '未识别')}}"},
	TriggerFileOperationCompleted: {"入库完成：{{anime.title}}",
		"{{release.title}}\n文件：{{out.name}}\n字幕：{{out.subtitles}}\n{{showIf(frt.group)}}原文件字幕组：{{frt.group}}"},
	TriggerFileOperationFailed: {"入库失败：{{anime.title}}",
		"{{release.title}}\n文件：{{file.name}}\n{{showIf(frt.group)}}原文件字幕组：{{frt.group}}\n原因：{{defaultIfEmpty(result.reason, '未知错误')}}"},
	TriggerReleaseDiscovered: {"发现新发布：{{anime.title}}",
		"{{release.title}}\n字幕组：{{defaultIfEmpty(anime.group, '未知')}}\n集数：{{coalesce(release.episodes, ep.display, '未识别')}}"},
	TriggerDownloadFailed: {"下载失败：{{anime.title}}",
		"{{release.title}}\n原因：{{if(eq(result.category, 'stalled'), '下载停滞', if(eq(result.category, 'timeout'), '等待超时', if(eq(result.category, 'client'), '下载客户端报错', if(eq(result.category, 'unobservable'), '无法读取下载状态', if(eq(result.category, 'occupied'), '下载器中已有同一种子的任务', '提交失败')))))}}{{if(result.retryable, '\n可在 RSS 页面重试。', '')}}"},
	TriggerSubscriptionChanged: {"{{if(eq(event.action, 'started'), '开始订阅', if(eq(event.action, 'stopped'), '已停止订阅', if(eq(event.action, 'resumed'), '已恢复订阅', '已退订')))}}：{{anime.title}}",
		"{{anime.title}}"},
	TriggerUnknownFilesImported: {"有未识别的文件入库：{{anime.title}}",
		"{{release.title}}\n{{out.count}} 个文件需要手动匹配：\n{{out.files}}"},
	TriggerReplacementDeletionFailed: {"旧版本未删除：{{anime.title}}",
		"{{file.name}}\n{{if(eq(result.category, 'read_only'), '文件所在位置只读，未能删除', if(eq(result.category, 'not_restored'), '旧文件已移到同一文件夹的隐藏目录，未能放回原位置', if(eq(result.category, 'path_occupied'), '旧文件已移到同一文件夹的隐藏目录，原位置已有同名文件', '文件可能被占用或没有权限')))}}"},
	TriggerReplacementApplied: {"已替换旧版本：{{anime.title}}",
		"{{release.title}}\n字幕组：{{defaultIfEmpty(anime.group, '未知')}}\n替换了 {{replaced.count}} 个旧版本：\n{{replaced.targets}}"},
	TriggerFileDisplaced: {"{{if(eq(event.cause, 'redownload'), '重新下载覆盖了旧文件', '新文件覆盖了旧文件')}}：{{anime.title}}",
		"{{showIf(taker.title)}}{{taker.title}}\n旧文件：{{file.name}}\n{{showIf(taker.file)}}新文件：{{taker.file}}\n{{showIf(frt.group)}}旧文件字幕组：{{frt.group}}\n{{showIf(taker.frt.group)}}新文件字幕组：{{taker.frt.group}}\n{{showIf(frt.res)}}旧文件分辨率：{{frt.res}}\n{{showIf(taker.frt.res)}}新文件分辨率：{{taker.frt.res}}\n{{showIf(frt.vc)}}旧文件视频编码：{{frt.vc}}\n{{showIf(taker.frt.vc)}}新文件视频编码：{{taker.frt.vc}}\n{{showIf(frt.ac)}}旧文件音频编码：{{frt.ac}}\n{{showIf(taker.frt.ac)}}新文件音频编码：{{taker.frt.ac}}"},
}

var defaultsEN = map[string][2]string{
	TriggerDownloadSubmitted: {"Download started: {{anime.title}}",
		"{{release.title}}\nGroup: {{defaultIfEmpty(anime.group, 'unknown')}}\nEpisode: {{defaultIfEmpty(ep.display, 'not recognized')}}"},
	TriggerFileOperationCompleted: {"Added to library: {{anime.title}}",
		"{{release.title}}\nFile: {{out.name}}\nSubtitles: {{out.subtitles}}\n{{showIf(frt.group)}}Original file group: {{frt.group}}"},
	TriggerFileOperationFailed: {"Library import failed: {{anime.title}}",
		"{{release.title}}\nFile: {{file.name}}\n{{showIf(frt.group)}}Original file group: {{frt.group}}\nReason: {{defaultIfEmpty(result.reason, 'unknown error')}}"},
	TriggerReleaseDiscovered: {"New release: {{anime.title}}",
		"{{release.title}}\nGroup: {{defaultIfEmpty(anime.group, 'unknown')}}\nEpisodes: {{coalesce(release.episodes, ep.display, 'not recognized')}}"},
	TriggerDownloadFailed: {"Download failed: {{anime.title}}",
		"{{release.title}}\nReason: {{if(eq(result.category, 'stalled'), 'download stalled', if(eq(result.category, 'timeout'), 'timed out', if(eq(result.category, 'client'), 'download client error', if(eq(result.category, 'unobservable'), 'status unavailable', if(eq(result.category, 'occupied'), 'the download client already has a task for this torrent', 'submission failed')))))}}{{if(result.retryable, '\nYou can retry it from the RSS page.', '')}}"},
	TriggerSubscriptionChanged: {"{{if(eq(event.action, 'started'), 'Subscribed', if(eq(event.action, 'stopped'), 'Subscription stopped', if(eq(event.action, 'resumed'), 'Subscription resumed', 'Unsubscribed')))}}: {{anime.title}}",
		"{{anime.title}}"},
	TriggerUnknownFilesImported: {"Unrecognized files added: {{anime.title}}",
		"{{release.title}}\n{{out.count}} file(s) need a manual match:\n{{out.files}}"},
	TriggerReplacementDeletionFailed: {"Old version not deleted: {{anime.title}}",
		"{{file.name}}\n{{if(eq(result.category, 'read_only'), 'The file is on a read-only location and was not deleted', if(eq(result.category, 'not_restored'), 'The old file was moved to a hidden folder next to it and could not be moved back', if(eq(result.category, 'path_occupied'), 'The old file was moved to a hidden folder next to it; another file now has its name', 'The file may be in use or not permitted to change')))}}"},
	TriggerReplacementApplied: {"Replaced an old version: {{anime.title}}",
		"{{release.title}}\nGroup: {{defaultIfEmpty(anime.group, 'unknown')}}\nReplaced {{replaced.count}} old version(s):\n{{replaced.targets}}"},
	TriggerFileDisplaced: {"{{if(eq(event.cause, 'redownload'), 'A redownload replaced the old file', 'A new file replaced the old file')}}: {{anime.title}}",
		"{{showIf(taker.title)}}{{taker.title}}\nOld file: {{file.name}}\n{{showIf(taker.file)}}New file: {{taker.file}}\n{{showIf(frt.group)}}Old file group: {{frt.group}}\n{{showIf(taker.frt.group)}}New file group: {{taker.frt.group}}\n{{showIf(frt.res)}}Old file resolution: {{frt.res}}\n{{showIf(taker.frt.res)}}New file resolution: {{taker.frt.res}}\n{{showIf(frt.vc)}}Old file video codec: {{frt.vc}}\n{{showIf(taker.frt.vc)}}New file video codec: {{taker.frt.vc}}\n{{showIf(frt.ac)}}Old file audio codec: {{frt.ac}}\n{{showIf(taker.frt.ac)}}New file audio codec: {{taker.frt.ac}}"},
}

// Sample returns the fixed redacted sample facts for a trigger, keyed by the
// unified paths. Unknown triggers return an empty map.
func Sample(trigger string) map[string]any {
	scenario, ok := ScenarioOf(trigger)
	if !ok {
		return map[string]any{}
	}
	facts := map[string]any{}
	for _, fact := range templatefacts.ScenarioFacts(scenario) {
		if !fact.Hidden {
			facts[fact.Path] = sampleFact(fact.SampleFor(scenario))
		}
	}
	if _, ok := facts["release.src"]; ok {
		facts[AniBTLinkVariable] = "https://anibt.net/release/10001"
		facts[MikanLinkVariable] = "https://mikanani.me/Home/Episode/0123456789abcdef0123456789abcdef01234567"
	}
	if _, ok := facts["release.id"]; ok {
		facts[DownloadLinkVariable] = "https://example.com/download/sample.torrent"
	}
	return facts
}

// ScenarioOf maps a trigger onto its registry scenario.
func ScenarioOf(trigger string) (templatefacts.Scenario, bool) {
	switch trigger {
	case TriggerDownloadSubmitted:
		return templatefacts.ScenarioDownloadSubmitted, true
	case TriggerFileOperationCompleted:
		return templatefacts.ScenarioFileCompleted, true
	case TriggerFileOperationFailed:
		return templatefacts.ScenarioFileFailed, true
	case TriggerReleaseDiscovered:
		return templatefacts.ScenarioReleaseDiscovered, true
	case TriggerDownloadFailed:
		return templatefacts.ScenarioDownloadFailed, true
	case TriggerSubscriptionChanged:
		return templatefacts.ScenarioSubscriptionChanged, true
	case TriggerUnknownFilesImported:
		return templatefacts.ScenarioUnknownImported, true
	case TriggerReplacementDeletionFailed:
		return templatefacts.ScenarioDeletionFailed, true
	case TriggerReplacementApplied:
		return templatefacts.ScenarioReplacementApplied, true
	case TriggerFileDisplaced:
		return templatefacts.ScenarioFileDisplaced, true
	default:
		return 0, false
	}
}

// sampleFact turns a registry sample into the value a render reads. String
// lists remain lists so standalone-list lines can repeat or omit their label.
func sampleFact(value any) any {
	switch typed := value.(type) {
	case []string:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = item
		}
		return out
	case []map[string]any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = item
		}
		return out
	}
	return value
}
