package businesslog

import (
	"database/sql"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"bansoko/internal/batchplan"
)

// The run log reads one acquisition's entries as one download run (log
// readability, 10-21): every run entry carries the same run context, frozen at
// write time by its writer, plus the concrete facts of its own step. Nothing
// here is derived at read time.

// RunContext is the run context of one acquisition's entries: the subgroup
// display name, the torrent title and the episode numbers of the frozen batch
// plan (the same projection the execution records use).
func RunContext(groupName, releaseTitle string, coverageKeys sql.NullString) map[string]any {
	values := map[string]any{}
	if name := strings.TrimSpace(groupName); name != "" {
		values["group_name"] = BoundText(name, 256)
	}
	if title := strings.TrimSpace(releaseTitle); title != "" {
		values["release_title"] = BoundText(title, 1024)
	}
	if episodes := PlanEpisodes(coverageKeys); len(episodes) > 0 {
		values["episodes"] = episodes
	}
	return values
}

// maxRunEpisodes bounds the episode list of one entry (contract maxItems).
const maxRunEpisodes = 500

// PlanEpisodes projects frozen batch-plan coverage keys (a JSON array of
// strings) onto ascending unique episode numbers. A plan without numbered TV
// episodes, an undecodable value or a list over the bound yields nil.
func PlanEpisodes(raw sql.NullString) []int {
	if !raw.Valid {
		return nil
	}
	var keys []string
	if json.Unmarshal([]byte(raw.String), &keys) != nil || len(keys) == 0 {
		return nil
	}
	plan := batchplan.Plan{CoverageKeys: keys}
	episodes, ok := plan.Episodes()
	if !ok {
		return nil
	}
	unique := make([]int, 0, len(episodes))
	for index, value := range episodes {
		if value < 0 {
			return nil
		}
		if index == 0 || value != episodes[index-1] {
			unique = append(unique, value)
		}
	}
	if len(unique) == 0 || len(unique) > maxRunEpisodes {
		return nil
	}
	return unique
}

// FileName is the last element of the first non-empty path: only the name is
// shown, never the private directory.
func FileName(paths ...string) string {
	for _, p := range paths {
		p = strings.TrimRight(strings.TrimSpace(p), "/")
		if p == "" {
			continue
		}
		if i := strings.LastIndex(p, "/"); i >= 0 {
			p = p[i+1:]
		}
		if p != "" {
			return BoundText(p, 1024)
		}
	}
	return ""
}

// LibraryFolder is the last two directory levels of a library target path
// ("Title/Season 2"), never the private root: the run log names where a file
// went without persisting an absolute path. Empty when the path has no
// directory.
func LibraryFolder(targetPath string) string {
	path := strings.TrimRight(strings.TrimSpace(targetPath), "/")
	index := strings.LastIndex(path, "/")
	if index <= 0 {
		return ""
	}
	segments := strings.FieldsFunc(path[:index], func(r rune) bool { return r == '/' })
	if len(segments) == 0 {
		return ""
	}
	if len(segments) > 2 {
		segments = segments[len(segments)-2:]
	}
	return BoundText(strings.Join(segments, "/"), 1024)
}

// MarshalDetails encodes detail values, omitting nil values, empty strings and
// empty lists; nothing left encodes as nil (no details).
func MarshalDetails(values map[string]any) (json.RawMessage, error) {
	for key, value := range values {
		switch typed := value.(type) {
		case nil:
			delete(values, key)
		case string:
			if typed == "" {
				delete(values, key)
			}
		case []string:
			if len(typed) == 0 {
				delete(values, key)
			}
		case []int:
			if len(typed) == 0 {
				delete(values, key)
			}
		}
	}
	if len(values) == 0 {
		return nil, nil
	}
	return json.Marshal(values)
}

// BoundText cuts text to max bytes without splitting a rune.
func BoundText(value string, max int) string {
	if len(value) <= max {
		return value
	}
	value = value[:max]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
