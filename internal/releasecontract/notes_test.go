package releasecontract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLintReleaseNotesAcceptsTheRenderedSubset(t *testing.T) {
	body := "## 新增\n- 新增一项\n- 另一项\n\n## 修复\n1. 修复一项\n\n## 注意\n更新前请先备份。\n"
	sections, err := LintReleaseNotes("zh-CN", body)
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 3 || sections[0] != 0 || sections[1] != 2 || sections[2] != 3 {
		t.Fatalf("sections: %v", sections)
	}
}

func TestLintReleaseNotesRefusesWhatTheRendererShowsLiterally(t *testing.T) {
	for name, body := range map[string]string{
		"empty":              "  \n",
		"text before":        "intro\n## Fixed\n- a\n",
		"h1":                 "# Fixed\n- a\n",
		"h3":                 "### Fixed\n- a\n",
		"unknown section":    "## Misc\n- a\n",
		"zh heading in en":   "## 修复\n- a\n",
		"out of order":       "## Fixed\n- a\n## New\n- b\n",
		"repeated":           "## Fixed\n- a\n## Fixed\n- b\n",
		"empty section":      "## New\n## Fixed\n- a\n",
		"empty last section": "## Fixed\n- a\n## Important\n",
		"html":               "## Fixed\n- a <b>bold</b>\n",
		"link":               "## Fixed\n- see [docs](https://example.com)\n",
		"image":              "## Fixed\n- ![x](y)\n",
		"code":               "## Fixed\n- run `x`\n",
		"emphasis":           "## Fixed\n- **a**\n",
		"quote":              "## Fixed\n> a\n",
		"star bullet":        "## Fixed\n* a\n",
		"nested":             "## Fixed\n- a\n  - b\n",
		"tab":                "## Fixed\n-\ta\n",
		"empty item":         "## Fixed\n- \n",
		"table":              "## Fixed\n| a | b |\n",
		"rule":               "## Fixed\n- a\n---\n",
		"fence":              "## Fixed\n```\nx\n```\n",
	} {
		if _, err := LintReleaseNotes("en", body); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := LintReleaseNotes("en", "## Fixed\n- "+strings.Repeat("a", 16<<10)+"\n"); err == nil {
		t.Error("oversize body accepted")
	}
	if _, err := LintReleaseNotes("fr", "## Fixed\n- a\n"); err == nil {
		t.Error("unsupported language accepted")
	}
}

func writeNotes(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func notesDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeNotes(t, dir, map[string]string{
		"versions.txt":        "# released versions\n0.1.0 2026-10-27\n0.1.1 2026-11-02\n0.2.0 2026-11-20\n",
		"unreleased.zh-CN.md": "",
		"unreleased.en.md":    "",
		"0.1.0.zh-CN.md":      "## 新增\n- 首个版本\n",
		"0.1.0.en.md":         "## New\n- First version\n",
		"0.1.1.zh-CN.md":      "## 修复\n- 修复一项\n",
		"0.1.1.en.md":         "## Fixed\n- Fixed one\n",
		"0.2.0.zh-CN.md":      "## 改进\n- 改进一项\n\n## 注意\n更新前请先备份。\n",
		"0.2.0.en.md":         "## Improved\n- Improved one\n\n## Important\nBack up before updating.\n",
	})
	return dir
}

func TestBuildReleaseNotesCarriesTheVersionAndItsHistory(t *testing.T) {
	dir := notesDir(t)
	entries, err := BuildReleaseNotes(dir, "0.1.1")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Version != "0.1.1" || entries[1].Version != "0.1.0" {
		t.Fatalf("entries: %+v", entries)
	}
	if got := entries[0].PublishedAt.Format("2006-01-02T15:04:05Z07:00"); got != "2026-11-02T00:00:00Z" {
		t.Fatalf("published_at: %s", got)
	}
	if entries[0].Notes.ZhCN != "## 修复\n- 修复一项\n" || entries[0].Notes.En != "## Fixed\n- Fixed one\n" {
		t.Fatalf("notes: %+v", entries[0].Notes)
	}
	body := GitHubReleaseBody(entries[0])
	if !strings.Contains(body, "- 修复一项") || !strings.Contains(body, "- Fixed one") {
		t.Fatalf("github body: %s", body)
	}
	all, err := BuildReleaseNotes(dir, "0.2.0")
	if err != nil || len(all) != 3 || all[0].Version != "0.2.0" {
		t.Fatalf("0.2.0: %+v %v", all, err)
	}
}

func TestBuildReleaseNotesFailsClosed(t *testing.T) {
	cases := map[string]func(dir string){
		"version not released": func(dir string) {},
		"missing language": func(dir string) {
			os.Remove(filepath.Join(dir, "0.1.1.en.md"))
		},
		"sections differ": func(dir string) {
			writeNotes(t, dir, map[string]string{"0.1.1.en.md": "## New\n- Fixed one\n"})
		},
		"unlisted version file": func(dir string) {
			writeNotes(t, dir, map[string]string{"0.3.0.zh-CN.md": "## 新增\n- a\n", "0.3.0.en.md": "## New\n- a\n"})
		},
		"index goes backwards": func(dir string) {
			writeNotes(t, dir, map[string]string{"versions.txt": "0.1.1 2026-11-02\n0.1.0 2026-10-27\n0.2.0 2026-11-20\n"})
		},
		"one-language unreleased": func(dir string) {
			writeNotes(t, dir, map[string]string{"unreleased.zh-CN.md": "## 新增\n- a\n"})
		},
		"invalid markup": func(dir string) {
			writeNotes(t, dir, map[string]string{"0.1.0.en.md": "## New\n- <script>\n"})
		},
	}
	for name, mutate := range cases {
		dir := notesDir(t)
		mutate(dir)
		version := "0.1.1"
		if name == "version not released" {
			version = "0.3.0"
		}
		if _, err := BuildReleaseNotes(dir, version); err == nil {
			t.Errorf("%s: built", name)
		}
	}
	if _, err := BuildReleaseNotes(notesDir(t), "0.1.1-rc.1"); err == nil {
		t.Error("prerelease version built")
	}
}

func TestBuildReleaseNotesKeepsAtMostTenVersions(t *testing.T) {
	dir := t.TempDir()
	index := ""
	files := map[string]string{"unreleased.zh-CN.md": "", "unreleased.en.md": ""}
	for minor := 1; minor <= 12; minor++ {
		version := "0." + itoa(minor) + ".0"
		index += version + " 2026-10-27\n"
		files[version+".zh-CN.md"] = "## 新增\n- a\n"
		files[version+".en.md"] = "## New\n- a\n"
	}
	files["versions.txt"] = index
	writeNotes(t, dir, files)
	entries, err := BuildReleaseNotes(dir, "0.12.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 10 || entries[0].Version != "0.12.0" || entries[9].Version != "0.3.0" {
		t.Fatalf("entries: %d first=%s last=%s", len(entries), entries[0].Version, entries[len(entries)-1].Version)
	}
}

func TestRepositoryReleaseNotesAreValid(t *testing.T) {
	if err := CheckReleaseNotes(filepath.Join("..", "..", "release-notes")); err != nil {
		t.Fatal(err)
	}
}

func itoa(value int) string {
	if value < 10 {
		return string(rune('0' + value))
	}
	return itoa(value/10) + string(rune('0'+value%10))
}
