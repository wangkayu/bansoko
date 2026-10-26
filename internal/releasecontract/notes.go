package releasecontract

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	hardeningdomain "bansoko/internal/hardening/domain"
)

// Release notes live in the repository as one Markdown file per version and
// language, plus an index that records which versions are released and when:
//
//	release-notes/versions.txt        "<X.Y.Z> <YYYY-MM-DD>" per line, oldest first
//	release-notes/<X.Y.Z>.zh-CN.md    notes of one released version
//	release-notes/<X.Y.Z>.en.md
//	release-notes/unreleased.zh-CN.md notes collected for the next version
//	release-notes/unreleased.en.md
//
// The bodies are the restricted Markdown the update page renders
// (frontend/src/ui/RestrictedMarkdown.tsx): fixed "## " section headings,
// list items and plain paragraphs. Everything else that renderer would show
// as literal text (HTML, links, images, emphasis, code, quotes, tables) is
// refused here, so the signed text always renders as written.
const (
	ReleaseNotesIndexName  = "versions.txt"
	ReleaseNotesUnreleased = "unreleased"
)

// ReleaseNotesLanguages are the required languages, in output order.
var ReleaseNotesLanguages = []string{"zh-CN", "en"}

// releaseNotesSections are the fixed section headings per language, in the
// order they must appear. Index i names the same section in both languages.
var releaseNotesSections = map[string][]string{
	"zh-CN": {"新增", "改进", "修复", "注意"},
	"en":    {"New", "Improved", "Fixed", "Important"},
}

var (
	releaseVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	notesIndexLine        = regexp.MustCompile(`^(\S+) ([0-9]{4}-[0-9]{2}-[0-9]{2})$`)
	notesOrderedItem      = regexp.MustCompile(`^[0-9]+\. (.*)$`)
	notesFileName         = regexp.MustCompile(`^(.+)\.(zh-CN|en)\.md$`)
)

// IsReleaseVersion reports a formal release version: plain MAJOR.MINOR.PATCH.
func IsReleaseVersion(version string) bool {
	return releaseVersionPattern.MatchString(version)
}

// LintReleaseNotes checks one language body and returns the indexes of the
// sections it contains, in order.
func LintReleaseNotes(language, body string) ([]int, error) {
	headings, ok := releaseNotesSections[language]
	if !ok {
		return nil, fmt.Errorf("unsupported release notes language %q", language)
	}
	if strings.TrimSpace(body) == "" {
		return nil, errors.New("release notes are empty")
	}
	if len(body) > hardeningdomain.MaxReleaseNotesLanguageBytes {
		return nil, fmt.Errorf("release notes exceed %d bytes", hardeningdomain.MaxReleaseNotesLanguageBytes)
	}
	if !utf8.ValidString(body) {
		return nil, errors.New("release notes are not valid UTF-8")
	}
	var sections []int
	open := false // a heading was seen and still has no content line
	for number, line := range strings.Split(strings.TrimSuffix(body, "\n"), "\n") {
		fail := func(reason string) ([]int, error) {
			return nil, fmt.Errorf("line %d: %s", number+1, reason)
		}
		for _, r := range line {
			if r != ' ' && (unicode.IsControl(r) || r == ' ' || r == ' ') {
				return fail("control characters and tabs are not allowed")
			}
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		if line != strings.TrimSpace(line) {
			return fail("leading or trailing spaces are not allowed (nested lists are not rendered)")
		}
		if strings.HasPrefix(line, "#") {
			title, found := strings.CutPrefix(line, "## ")
			if !found {
				return fail("only \"## <section>\" headings are allowed")
			}
			index := indexOf(headings, title)
			if index < 0 {
				return fail(fmt.Sprintf("unknown section %q; allowed: %s", title, strings.Join(headings, ", ")))
			}
			if open {
				return fail("the previous section is empty")
			}
			if len(sections) > 0 && index <= sections[len(sections)-1] {
				return fail("sections must appear once each, in the order " + strings.Join(headings, ", "))
			}
			sections = append(sections, index)
			open = true
			continue
		}
		if len(sections) == 0 {
			return fail("text must follow a section heading")
		}
		text := line
		if item, found := strings.CutPrefix(line, "- "); found {
			text = item
		} else if match := notesOrderedItem.FindStringSubmatch(line); match != nil {
			text = match[1]
		}
		if strings.TrimSpace(text) == "" {
			return fail("empty list item")
		}
		if reason := unsupportedMarkdown(line, text); reason != "" {
			return fail(reason)
		}
		open = false
	}
	if open {
		return nil, errors.New("the last section is empty")
	}
	return sections, nil
}

// unsupportedMarkdown names a construct the restricted renderer would show as
// literal text instead of interpreting it.
func unsupportedMarkdown(line, text string) string {
	for _, prefix := range []string{">", "|", "* ", "+ ", "---", "***", "===", "```", "~~~"} {
		if strings.HasPrefix(line, prefix) {
			return fmt.Sprintf("%q blocks are not rendered; use \"- \" items and plain paragraphs", prefix)
		}
	}
	for _, token := range []string{"<", "`", "](", "![", "**", "__", "~~", "\\"} {
		if strings.Contains(text, token) {
			return fmt.Sprintf("%q is not rendered (no HTML, code, links, images or emphasis)", token)
		}
	}
	return ""
}

func indexOf(values []string, value string) int {
	for index, candidate := range values {
		if candidate == value {
			return index
		}
	}
	return -1
}

// readNotesPair reads and lints both language files of one version name and
// proves they carry the same sections.
func readNotesPair(dir, name string) (hardeningdomain.ReleaseNotesText, error) {
	bodies := map[string]string{}
	var reference []int
	for _, language := range ReleaseNotesLanguages {
		path := filepath.Join(dir, name+"."+language+".md")
		data, err := os.ReadFile(path)
		if err != nil {
			return hardeningdomain.ReleaseNotesText{}, fmt.Errorf("release notes %s.%s.md: %w", name, language, err)
		}
		body := strings.TrimSpace(string(data))
		sections, err := LintReleaseNotes(language, body)
		if err != nil {
			return hardeningdomain.ReleaseNotesText{}, fmt.Errorf("release notes %s.%s.md: %w", name, language, err)
		}
		if reference == nil {
			reference = sections
		} else if !equalInts(reference, sections) {
			return hardeningdomain.ReleaseNotesText{}, fmt.Errorf("release notes %s: zh-CN and en do not carry the same sections", name)
		}
		bodies[language] = body + "\n"
	}
	return hardeningdomain.ReleaseNotesText{ZhCN: bodies["zh-CN"], En: bodies["en"]}, nil
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}

type notesIndexEntry struct {
	version   string
	semver    hardeningdomain.SemVer
	published time.Time
}

// readNotesIndex reads versions.txt: released versions, oldest first, strictly
// increasing, each with its release date.
func readNotesIndex(dir string) ([]notesIndexEntry, error) {
	data, err := os.ReadFile(filepath.Join(dir, ReleaseNotesIndexName))
	if err != nil {
		return nil, fmt.Errorf("release notes index: %w", err)
	}
	var entries []notesIndexEntry
	scanner := bufio.NewScanner(bytes.NewReader(data))
	number := 0
	for scanner.Scan() {
		number++
		line := scanner.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		match := notesIndexLine.FindStringSubmatch(line)
		if match == nil || !IsReleaseVersion(match[1]) {
			return nil, fmt.Errorf("%s line %d: expected \"<X.Y.Z> <YYYY-MM-DD>\"", ReleaseNotesIndexName, number)
		}
		published, err := time.Parse("2006-01-02", match[2])
		if err != nil {
			return nil, fmt.Errorf("%s line %d: invalid date", ReleaseNotesIndexName, number)
		}
		semver, err := hardeningdomain.ParseSemVer(match[1])
		if err != nil {
			return nil, fmt.Errorf("%s line %d: %w", ReleaseNotesIndexName, number, err)
		}
		if n := len(entries); n > 0 {
			last := entries[n-1]
			if hardeningdomain.CompareSemVer(semver, last.semver) <= 0 {
				return nil, fmt.Errorf("%s line %d: versions must increase from top to bottom", ReleaseNotesIndexName, number)
			}
			if published.Before(last.published) {
				return nil, fmt.Errorf("%s line %d: release dates must not go backwards", ReleaseNotesIndexName, number)
			}
		}
		entries = append(entries, notesIndexEntry{version: match[1], semver: semver, published: published.UTC()})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

// CheckReleaseNotes validates the whole release-notes directory: the index,
// every released version's pair, no unlisted version files, and the
// unreleased pair (which may be empty while nothing user-visible changed).
func CheckReleaseNotes(dir string) error {
	index, err := readNotesIndex(dir)
	if err != nil {
		return err
	}
	listed := map[string]bool{}
	for _, entry := range index {
		listed[entry.version] = true
		if _, err := readNotesPair(dir, entry.version); err != nil {
			return err
		}
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, file := range files {
		name := file.Name()
		if name == ReleaseNotesIndexName {
			continue
		}
		match := notesFileName.FindStringSubmatch(name)
		if match == nil || file.IsDir() {
			return fmt.Errorf("release notes: unexpected entry %q", name)
		}
		if match[1] != ReleaseNotesUnreleased && !listed[match[1]] {
			return fmt.Errorf("release notes: %s is not listed in %s", name, ReleaseNotesIndexName)
		}
	}
	empty := 0
	for _, language := range ReleaseNotesLanguages {
		data, err := os.ReadFile(filepath.Join(dir, ReleaseNotesUnreleased+"."+language+".md"))
		if err != nil {
			return fmt.Errorf("release notes %s.%s.md: %w", ReleaseNotesUnreleased, language, err)
		}
		if strings.TrimSpace(string(data)) == "" {
			empty++
		}
	}
	switch empty {
	case len(ReleaseNotesLanguages):
		return nil
	case 0:
		_, err := readNotesPair(dir, ReleaseNotesUnreleased)
		return err
	default:
		return errors.New("release notes: unreleased notes exist in only one language")
	}
}

// BuildReleaseNotes returns the notes a manifest for version carries: the
// version's own entry first, then earlier released versions, newest first,
// within the entry and size bounds. A version that is not released in the
// index, or whose notes are missing or invalid, fails closed.
func BuildReleaseNotes(dir, version string) ([]hardeningdomain.ReleaseNotesEntry, error) {
	if !IsReleaseVersion(version) {
		return nil, fmt.Errorf("release notes are built only for X.Y.Z versions, not %q", version)
	}
	if err := CheckReleaseNotes(dir); err != nil {
		return nil, err
	}
	index, err := readNotesIndex(dir)
	if err != nil {
		return nil, err
	}
	target, err := hardeningdomain.ParseSemVer(version)
	if err != nil {
		return nil, err
	}
	var selected []notesIndexEntry
	found := false
	for _, entry := range index {
		comparison := hardeningdomain.CompareSemVer(entry.semver, target)
		if comparison == 0 {
			found = true
		}
		if comparison <= 0 {
			selected = append(selected, entry)
		}
	}
	if !found {
		return nil, fmt.Errorf("release notes: %s has no entry for %s; move the unreleased notes to this version first", ReleaseNotesIndexName, version)
	}
	sort.SliceStable(selected, func(i, j int) bool {
		return hardeningdomain.CompareSemVer(selected[i].semver, selected[j].semver) > 0
	})
	var result []hardeningdomain.ReleaseNotesEntry
	total := 0
	for _, entry := range selected {
		if len(result) == hardeningdomain.MaxReleaseNotesEntries {
			break
		}
		text, err := readNotesPair(dir, entry.version)
		if err != nil {
			return nil, err
		}
		size := len(text.ZhCN) + len(text.En)
		if total+size > hardeningdomain.MaxReleaseNotesTotalBytes {
			if len(result) == 0 {
				return nil, fmt.Errorf("release notes of %s exceed the manifest budget", version)
			}
			break
		}
		total += size
		result = append(result, hardeningdomain.ReleaseNotesEntry{Version: entry.version, PublishedAt: entry.published, Notes: text})
	}
	if err := hardeningdomain.ValidateReleaseNotes(version, result); err != nil {
		return nil, err
	}
	return result, nil
}

// GitHubReleaseBody renders one version's notes as the GitHub Release text:
// the same bodies the signed manifest carries, both languages.
func GitHubReleaseBody(entry hardeningdomain.ReleaseNotesEntry) string {
	var builder strings.Builder
	builder.WriteString("# 简体中文\n\n")
	builder.WriteString(strings.TrimSpace(entry.Notes.ZhCN))
	builder.WriteString("\n\n# English\n\n")
	builder.WriteString(strings.TrimSpace(entry.Notes.En))
	builder.WriteString("\n")
	return builder.String()
}
