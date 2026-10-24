package application

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"unicode"
)

const maxNotificationFactLength = 512

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
	// locationRoot is the publication location's own root: the directory a
	// target path is expressed relative to in the notification text.
	locationRoot string
	targetPath   string
	operation    string
	collision    string
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
	relativePath, outputDirectory, outputFilename := notificationOutputPath(facts.locationRoot, facts.targetPath, fileName)
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
		"out.path":             relativePath,
		"op.kind":              notificationText(facts.operation),
		"op.conflict":          notificationText(facts.collision),
	}
	for key, value := range facts.frozen {
		payload[key] = value
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
	stringValue, ok := value.(string)
	if !ok {
		return nil
	}
	return notificationText(stringValue)
}

func notificationText(value string) any {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxNotificationFactLength {
		return nil
	}
	value = strings.TrimSpace(safeString(value))
	if value == "" || len(value) > maxNotificationFactLength || filepath.IsAbs(value) || strings.Contains(value, "://") {
		return nil
	}
	for _, marker := range []string{"password=", "passwd=", "token=", "secret=", "credential=", "authorization=", "cookie="} {
		if strings.Contains(strings.ToLower(value), marker) {
			return nil
		}
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return nil
		}
	}
	return value
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

func notificationOutputPath(root, target, fallbackName any) (any, any, any) {
	rootString, _ := root.(string)
	targetString, _ := target.(string)
	fallback, _ := fallbackName.(string)
	if filepath.IsAbs(rootString) && filepath.IsAbs(targetString) {
		relative, err := filepath.Rel(filepath.Clean(rootString), filepath.Clean(targetString))
		if err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			relative = filepath.ToSlash(relative)
			if notificationText(relative) != nil {
				directory := filepath.ToSlash(filepath.Dir(relative))
				if directory == "." {
					directory = ""
				}
				return notificationText(relative), notificationText(directory), notificationText(filepath.Base(relative))
			}
		}
	}
	if fallback != "" {
		return nil, nil, notificationText(fallback)
	}
	return nil, nil, nil
}
