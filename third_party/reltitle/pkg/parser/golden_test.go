package parser

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/wangkayu/reltitle/pkg/config"
)

// The golden corpus pins the complete parse output for a few hundred real titles,
// so a rule or refactoring change shows exactly which titles changed.
//
//	go test ./pkg/parser -run TestGoldenCorpus          # verify
//	go test ./pkg/parser -run TestGoldenCorpus -update  # accept new output, then review the diff
var updateGolden = flag.Bool("update", false, "rewrite testdata/golden.jsonl from testdata/corpus.txt")

const (
	goldenCorpusPath = "testdata/corpus.txt"
	goldenFilePath   = "testdata/golden.jsonl"
	goldenMaxShown   = 10
)

type goldenRecord struct {
	Title  string          `json:"title"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

func TestGoldenCorpus(t *testing.T) {
	titles := readGoldenCorpus(t)

	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	actual := make([]goldenRecord, 0, len(titles))
	for _, title := range titles {
		rec := goldenRecord{Title: title}
		res, err := p.Parse(title)
		if err != nil {
			rec.Error = err.Error()
		} else {
			b, err := json.Marshal(res)
			if err != nil {
				t.Fatalf("marshal %q: %v", title, err)
			}
			rec.Result = b
		}
		actual = append(actual, rec)
	}

	if *updateGolden {
		writeGolden(t, actual)
		t.Logf("wrote %d records to %s", len(actual), goldenFilePath)
		return
	}

	expected := readGolden(t)
	want := make(map[string]goldenRecord, len(expected))
	for _, rec := range expected {
		want[rec.Title] = rec
	}

	var changed, missing []string
	seen := make(map[string]bool, len(actual))
	var report strings.Builder
	for _, got := range actual {
		seen[got.Title] = true
		exp, ok := want[got.Title]
		if !ok {
			missing = append(missing, got.Title)
			continue
		}
		if got.Error == exp.Error && bytes.Equal(compactJSON(got.Result), compactJSON(exp.Result)) {
			continue
		}
		changed = append(changed, got.Title)
		if len(changed) <= goldenMaxShown {
			fmt.Fprintf(&report, "\n=== %s\n%s\n", got.Title, goldenDiff(exp, got))
		}
	}
	var stale []string
	for _, rec := range expected {
		if !seen[rec.Title] {
			stale = append(stale, rec.Title)
		}
	}

	if len(changed) > 0 {
		if len(changed) > goldenMaxShown {
			fmt.Fprintf(&report, "\n... and %d more changed titles\n", len(changed)-goldenMaxShown)
		}
		t.Errorf("%d title(s) changed output:%s", len(changed), report.String())
	}
	if len(missing) > 0 {
		t.Errorf("%d corpus title(s) have no golden record (first: %q)", len(missing), missing[0])
	}
	if len(stale) > 0 {
		t.Errorf("%d golden record(s) no longer in the corpus (first: %q)", len(stale), stale[0])
	}
	if len(changed)+len(missing)+len(stale) > 0 {
		t.Log("if the new output is intended, run: go test ./pkg/parser -run TestGoldenCorpus -update, then review the golden.jsonl diff")
	}
}

func readGoldenCorpus(t testing.TB) []string {
	t.Helper()
	f, err := os.Open(goldenCorpusPath)
	if err != nil {
		t.Fatalf("open corpus: %v", err)
	}
	defer f.Close()

	var titles []string
	seen := make(map[string]bool)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if seen[line] {
			t.Fatalf("duplicate corpus title: %q", line)
		}
		seen[line] = true
		titles = append(titles, line)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	if len(titles) == 0 {
		t.Fatal("corpus is empty")
	}
	return titles
}

func readGolden(t *testing.T) []goldenRecord {
	t.Helper()
	f, err := os.Open(goldenFilePath)
	if err != nil {
		t.Fatalf("open golden file (run with -update to create it): %v", err)
	}
	defer f.Close()

	var out []goldenRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var rec goldenRecord
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatalf("decode golden record: %v", err)
		}
		out = append(out, rec)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read golden file: %v", err)
	}
	return out
}

func writeGolden(t *testing.T, recs []goldenRecord) {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	for _, rec := range recs {
		rec.Result = compactJSON(rec.Result)
		if err := enc.Encode(rec); err != nil {
			t.Fatalf("encode golden record: %v", err)
		}
	}
	if err := os.WriteFile(goldenFilePath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write golden file: %v", err)
	}
}

func compactJSON(b json.RawMessage) []byte {
	if len(b) == 0 {
		return nil
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, b); err != nil {
		return b
	}
	return buf.Bytes()
}

func indentJSON(rec goldenRecord) []string {
	if rec.Error != "" {
		return []string{"error: " + rec.Error}
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, rec.Result, "", "  "); err != nil {
		return []string{string(rec.Result)}
	}
	return strings.Split(buf.String(), "\n")
}

// goldenDiff renders a line diff (LCS based) between the pretty-printed expected and
// actual results, showing only the changed lines.
func goldenDiff(want, got goldenRecord) string {
	a, b := indentJSON(want), indentJSON(got)
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var out []string
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, "- "+a[i])
			i++
		default:
			out = append(out, "+ "+b[j])
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, "- "+a[i])
	}
	for ; j < m; j++ {
		out = append(out, "+ "+b[j])
	}
	return strings.Join(out, "\n")
}
