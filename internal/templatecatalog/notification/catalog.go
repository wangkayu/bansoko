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
	"strings"

	"bansoko/internal/templatefacts"
)

const (
	LanguageVersion = "bansoko-template-1"
	// CatalogVersion is the notification template catalog version.
	CatalogVersion = "notification-catalog-v1"

	TriggerDownloadSubmitted      = "download_submitted"
	TriggerFileOperationCompleted = "file_operation_completed"
	TriggerFileOperationFailed    = "file_operation_failed"
	TriggerReleaseDiscovered      = "release_discovered"
	TriggerDownloadFailed         = "download_failed"
	TriggerSubscriptionChanged    = "subscription_changed"
	TriggerUnknownFilesImported   = "unknown_files_imported"

	// LocaleZH and LocaleEN select the language of the system default
	// templates.
	LocaleZH = "zh-CN"
	LocaleEN = "en"
)

// Triggers lists the closed notification trigger registry in catalog order.
var Triggers = []string{
	TriggerDownloadSubmitted, TriggerFileOperationCompleted, TriggerFileOperationFailed,
	TriggerReleaseDiscovered, TriggerDownloadFailed, TriggerSubscriptionChanged, TriggerUnknownFilesImported,
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
	return variables, true
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
	return pair[0], pair[1]
}

var defaultsZH = map[string][2]string{
	TriggerDownloadSubmitted: {"已加入下载：{{anime.title}}",
		"{{release.title}}\n字幕组：{{defaultIfEmpty(anime.group, '未知')}}\n集数：{{defaultIfEmpty(ep.display, '未识别')}}"},
	TriggerFileOperationCompleted: {"入库完成：{{anime.title}}",
		"{{release.title}}\n文件：{{out.name}}\n位置：{{out.path}}"},
	TriggerFileOperationFailed: {"入库失败：{{anime.title}}",
		"{{release.title}}\n原因：{{defaultIfEmpty(result.reason, '未知错误')}}"},
	TriggerReleaseDiscovered: {"发现新发布：{{anime.title}}",
		"{{release.title}}\n字幕组：{{defaultIfEmpty(anime.group, '未知')}}\n集数：{{coalesce(release.episodes, ep.display, '未识别')}}"},
	TriggerDownloadFailed: {"下载失败：{{anime.title}}",
		"{{release.title}}\n原因：{{if(eq(result.category, 'stalled'), '下载停滞', if(eq(result.category, 'timeout'), '等待超时', if(eq(result.category, 'client'), '下载客户端报错', if(eq(result.category, 'unobservable'), '无法读取下载状态', if(eq(result.category, 'occupied'), '下载器中已有同一种子的任务', '提交失败')))))}}{{if(result.retryable, '\n可在 RSS 页面重试。', '')}}"},
	TriggerSubscriptionChanged: {"{{if(eq(event.action, 'started'), '开始订阅', if(eq(event.action, 'stopped'), '已停止订阅', if(eq(event.action, 'resumed'), '已恢复订阅', '已退订')))}}：{{anime.title}}",
		"{{anime.title}}"},
	TriggerUnknownFilesImported: {"有未识别的文件入库：{{anime.title}}",
		"{{release.title}}\n{{out.count}} 个文件需要手动匹配：\n{{out.files}}"},
}

var defaultsEN = map[string][2]string{
	TriggerDownloadSubmitted: {"Download started: {{anime.title}}",
		"{{release.title}}\nGroup: {{defaultIfEmpty(anime.group, 'unknown')}}\nEpisode: {{defaultIfEmpty(ep.display, 'not recognized')}}"},
	TriggerFileOperationCompleted: {"Added to library: {{anime.title}}",
		"{{release.title}}\nFile: {{out.name}}\nLocation: {{out.path}}"},
	TriggerFileOperationFailed: {"Library import failed: {{anime.title}}",
		"{{release.title}}\nReason: {{defaultIfEmpty(result.reason, 'unknown error')}}"},
	TriggerReleaseDiscovered: {"New release: {{anime.title}}",
		"{{release.title}}\nGroup: {{defaultIfEmpty(anime.group, 'unknown')}}\nEpisodes: {{coalesce(release.episodes, ep.display, 'not recognized')}}"},
	TriggerDownloadFailed: {"Download failed: {{anime.title}}",
		"{{release.title}}\nReason: {{if(eq(result.category, 'stalled'), 'download stalled', if(eq(result.category, 'timeout'), 'timed out', if(eq(result.category, 'client'), 'download client error', if(eq(result.category, 'unobservable'), 'status unavailable', if(eq(result.category, 'occupied'), 'the download client already has a task for this torrent', 'submission failed')))))}}{{if(result.retryable, '\nYou can retry it from the RSS page.', '')}}"},
	TriggerSubscriptionChanged: {"{{if(eq(event.action, 'started'), 'Subscribed', if(eq(event.action, 'stopped'), 'Subscription stopped', if(eq(event.action, 'resumed'), 'Subscription resumed', 'Unsubscribed')))}}: {{anime.title}}",
		"{{anime.title}}"},
	TriggerUnknownFilesImported: {"Unrecognized files added: {{anime.title}}",
		"{{release.title}}\n{{out.count}} file(s) need a manual match:\n{{out.files}}"},
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
	default:
		return 0, false
	}
}

// sampleFact turns a registry sample into the value a render reads: a string
// list renders comma-separated, a file list keeps its entries.
func sampleFact(value any) any {
	switch typed := value.(type) {
	case []string:
		return strings.Join(typed, ", ")
	case []map[string]any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = item
		}
		return out
	}
	return value
}
