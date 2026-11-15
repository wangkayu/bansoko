package application

import (
	"database/sql"
	"encoding/json"
	"path"
	"path/filepath"
	"strings"

	plugintargetport "bansoko/internal/plugintarget/port"
	"bansoko/internal/templatecatalog/notification"
)

type fileOperationNotificationFacts struct {
	managedAnimeID     string
	animeTitle         string
	titleOverride      sql.NullString
	releaseID          string
	releaseTitle       string
	releaseRawFacts    []byte
	releaseSourceFacts []byte
	releaseParsedFacts []byte
	filePath           string
	fileSize           int64
	fileMediaKind      string
	fileFacts          []byte
	configSnapshot     []byte
	renderSnapshot     []byte
	// targetPath is the final placement path from the publication proof. The
	// output facts are projected from it, never from the naming template or the
	// original download name.
	targetPath string
	operation  string
	collision  string
	// frozen are catalog facts projected from frozen inputs (acquisition
	// input snapshot, file fact); they override the row-derived values.
	frozen map[string]any
}

func buildFileOperationNotificationPayload(facts fileOperationNotificationFacts, trigger, outcome, reasonCode, reason string) (json.RawMessage, error) {
	animeTitle := notificationText(facts.animeTitle)
	customTitle := notificationText(facts.titleOverride.String)
	if customTitle != nil {
		animeTitle = customTitle
	}
	releaseTitle := notificationText(facts.releaseTitle)
	fileName := notificationText(filepath.Base(filepath.FromSlash(facts.filePath)))
	fileBase, fileExtension := notificationFileParts(facts.filePath)
	fileFacts := notificationMediaFacts(facts.fileFacts)
	outputPath, outputDirectory, outputFilename := notificationOutputPath(facts.targetPath)
	resultReason := safeString(reason)
	if resultReason == "" {
		resultReason = boundedCode(reasonCode, trigger)
	}

	payload := map[string]any{
		"anime.id":             notificationText(facts.managedAnimeID),
		"anime.title":          animeTitle,
		"anime.title_override": customTitle,
		"release.id":           notificationText(facts.releaseID),
		"release.title":        releaseTitle,
		"event.type":           trigger,
		"result.outcome":       outcome,
		"result.reason":        resultReason,
		"file.name":            fileName,
		"file.stem":            fileBase,
		"file.ext":             fileExtension,
		"file.size":            facts.fileSize,
		"file.kind":            notificationText(facts.fileMediaKind),
		"media.container":      fileFacts["container"],
		"media.resolution":     fileFacts["resolution"],
		"media.video_codec":    fileFacts["video_codec"],
		"media.audio_codec":    fileFacts["audio_codec"],
		"media.hdr":            fileFacts["hdr"],
		"out.dir":              outputDirectory,
		"out.name":             outputFilename,
		"out.path":             outputPath,
		"op.kind":              notificationText(facts.operation),
		"op.conflict":          notificationText(facts.collision),
	}
	for key, value := range facts.frozen {
		payload[key] = value
	}
	if trigger == plugintargetport.TriggerFileOperationCompleted {
		// The SQL group-release function replaces this empty list with the
		// final bound-subtitle file names after every bound operation settles.
		payload["out.subtitles"] = []string{}
	}
	return json.Marshal(payload)
}

func notificationMediaFacts(raw []byte) map[string]any {
	var facts map[string]any
	if json.Unmarshal(raw, &facts) != nil {
		return map[string]any{}
	}
	result := make(map[string]any, 5)
	for _, key := range []string{"container", "resolution", "video_codec", "audio_codec", "hdr"} {
		result[key] = notificationTextValue(facts[key])
	}
	return result
}

func notificationTextValue(value any) any {
	return notification.TextValue(value)
}

func notificationText(value string) any {
	return notification.Text(value)
}

func notificationFileParts(path string) (any, any) {
	name := filepath.Base(filepath.FromSlash(path))
	if notificationText(name) == nil {
		return nil, nil
	}
	extension := strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
	base := strings.TrimSuffix(name, filepath.Ext(name))
	return notificationText(base), notificationText(extension)
}

// notificationOutputPath projects the final placement path onto the output
// facts: out.path is the absolute path, out.dir its absolute parent and
// out.name its basename. All three are absent when the publication proof has no
// usable absolute target; the original download name is never substituted for
// the final name. The SQL twin is file_success_notification (migration 136).
func notificationOutputPath(target any) (any, any, any) {
	targetString, _ := target.(string)
	if targetString == "" || !path.IsAbs(targetString) {
		return nil, nil, nil
	}
	return notification.Path(targetString), notification.Path(path.Dir(targetString)), notification.Name(path.Base(targetString))
}
