package agentguide_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"bansoko/api"
	"bansoko/internal/agentguide"
	animedomain "bansoko/internal/anime/domain"
	animetransport "bansoko/internal/anime/transport"
	"bansoko/internal/attrrules"
	"bansoko/internal/namingvars"
	"bansoko/internal/transport/httpapi/generated"
	"bansoko/internal/transport/mcp"
)

var (
	fencedBlock = regexp.MustCompile("(?s)```.*?```")
	codeSpan    = regexp.MustCompile("`([^`\n]+)`")
	snakeCase   = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)+$`)
	camelCase   = regexp.MustCompile(`^[a-z]+([A-Z][A-Za-z0-9]*)+$`)
	upperSnake  = regexp.MustCompile(`^[A-Z][A-Z0-9]*(_[A-Z0-9]+)+$`)
)

type spec struct {
	operationIDs map[string]bool
	words        map[string]bool
}

// loadSpec collects every operationId and every property, parameter and enum
// word of the OpenAPI document: the vocabulary the guide may use.
func loadSpec(t *testing.T) spec {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal(api.OpenAPI, &doc); err != nil {
		t.Fatal(err)
	}
	result := spec{operationIDs: map[string]bool{}, words: map[string]bool{}}
	var walk func(node any, key string)
	walk = func(node any, key string) {
		switch typed := node.(type) {
		case map[string]any:
			if id, ok := typed["operationId"].(string); ok {
				result.operationIDs[id] = true
			}
			if name, ok := typed["name"].(string); ok && typed["in"] != nil {
				result.words[name] = true
			}
			if props, ok := typed["properties"].(map[string]any); ok {
				for name := range props {
					result.words[name] = true
				}
			}
			if enum, ok := typed["enum"].([]any); ok {
				for _, value := range enum {
					if s, ok := value.(string); ok {
						result.words[s] = true
					}
				}
			}
			for k, v := range typed {
				walk(v, k)
			}
		case []any:
			for _, v := range typed {
				walk(v, key)
			}
		}
	}
	walk(doc, "")
	return result
}

// Words the guide uses that are not OpenAPI fields: arguments MCP synthesizes
// from headers, result keys the MCP layer adds, and reason codes quoted from
// tool descriptions.
var extraWords = []string{
	"idempotency_key", "expected_version", "timeout_seconds", "placement_inside_content",
	"snapshot_required", "not_started", "locator_display", "metadata_hold", "failed_work_item_id",
	"http_status", "exit_code", "request_id", "current_version", "candidate_revision",
	"current_input", "saved_revision", "owner_draft", "owner_chain", "agent_guide",
	"seed_not_expanded", "torrent_fetch_failed", "torrent_parse_failed", "torrent_missing",
	"target_duplicate", "placement_not_active", "file_not_missing", "source_release_unavailable",
	"download_in_progress", "scan_changed",
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestGuideIsEnglishMarkdownWithTheRequiredSections(t *testing.T) {
	guide := agentguide.Guide()
	if !strings.HasPrefix(guide, "# Bansoko agent guide\n") {
		t.Fatal("the guide must start with its title")
	}
	for _, heading := range []string{
		"## 1. Ways to operate the instance", "## 2. Concepts", "## 3. Safety rules",
		"## 4. Versions, idempotency and retries", "## 5. Task recipes", "## 6. Repair is re-enqueue",
		"## 7. Errors and exit codes", "### 5.1 Subscription", "### 5.2 One-shot", "### 5.3 File management",
		"### 5.4 Execution records", "### 5.5 Configuration",
	} {
		if !strings.Contains(guide, heading) {
			t.Errorf("guide is missing the section %q", heading)
		}
	}
	for _, r := range guide {
		if r > 0x7e && r != '\n' {
			t.Fatalf("the guide is agent-facing English text; found %q", r)
		}
	}
}

func TestSkillWrapsTheSameGuide(t *testing.T) {
	skill := agentguide.Skill("custom-name")
	prefix := "---\nname: custom-name\ndescription: "
	if !strings.HasPrefix(skill, prefix) || !strings.HasSuffix(skill, "\n---\n\n"+agentguide.Guide()) {
		t.Fatalf("skill must be front matter plus the verbatim guide: %q", skill[:80])
	}
	front := strings.SplitN(skill, "\n---\n", 2)[0]
	var meta map[string]string
	if err := yaml.Unmarshal([]byte(strings.TrimPrefix(front, "---\n")), &meta); err != nil || meta["name"] != "custom-name" || len(meta["description"]) < 40 {
		t.Fatalf("front matter = %v (%v)", meta, err)
	}
	if !strings.HasPrefix(agentguide.Skill(""), "---\nname: "+agentguide.SkillName+"\n") {
		t.Fatal("an empty name falls back to the default skill name")
	}
}

// Every MCP tool, operationId and error code the guide names must exist, and
// every allowlisted tool must appear in the guide.
func TestGuideReferencesExist(t *testing.T) {
	vocabulary := loadSpec(t)
	catalog, err := mcp.LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	tools := map[string]bool{}
	for _, name := range catalog.ToolNames() {
		tools[name] = true
	}
	for _, word := range extraWords {
		vocabulary.words[word] = true
	}
	prose := fencedBlock.ReplaceAllString(agentguide.Guide(), "")
	mentioned := map[string]bool{}
	sourceText := goSources(t)
	for _, match := range codeSpan.FindAllStringSubmatch(prose, -1) {
		token := match[1]
		switch {
		case snakeCase.MatchString(token):
			if tools[token] {
				mentioned[token] = true
			} else if !vocabulary.words[token] {
				t.Errorf("`%s` is neither an MCP tool nor an OpenAPI field/parameter/enum value (typo? add it to extraWords if it is a real non-contract word)", token)
			}
		case camelCase.MatchString(token):
			if !vocabulary.operationIDs[token] && token != "operationId" {
				t.Errorf("`%s` looks like an operationId but no such operation exists", token)
			}
		case upperSnake.MatchString(token):
			if !strings.Contains(sourceText, `"`+token+`"`) {
				t.Errorf("error code `%s` appears nowhere in the server sources or the OpenAPI document", token)
			}
		}
	}
	for name := range tools {
		if !mentioned[name] {
			t.Errorf("MCP tool %s is not mentioned in the guide", name)
		}
	}
}

// goSources concatenates the non-test Go sources under internal and the
// OpenAPI document, so an error code can be looked up as a quoted string.
func goSources(t *testing.T) string {
	t.Helper()
	var buffer bytes.Buffer
	buffer.Write(api.OpenAPI)
	err := filepath.Walk(filepath.Join(repoRoot(t), "internal"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.Contains(path, string(filepath.Separator)+"generated"+string(filepath.Separator)) {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		buffer.Write(raw)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return buffer.String()
}

// The recipe tables name a tool and the operation it runs on; they must agree
// with the OpenAPI allowlist.
func TestRecipeTablesAgreeWithTheAllowlist(t *testing.T) {
	vocabulary := loadSpec(t)
	catalog, err := mcp.LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	operationOf := map[string][]string{}
	for _, op := range catalog.Operations {
		operationOf[op.Tool] = []string{op.ID}
	}
	for _, comp := range catalog.Composites {
		operationOf[comp.Tool] = comp.Operations
	}
	rows := 0
	inTable := false
	for _, line := range strings.Split(agentguide.Guide(), "\n") {
		if strings.HasPrefix(line, "| Step | MCP tool | operationId | CLI |") {
			inTable = true
			continue
		}
		if !strings.HasPrefix(line, "|") {
			inTable = false
			continue
		}
		if !inTable || strings.HasPrefix(line, "|---") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) != 4 {
			t.Errorf("recipe row must have four cells: %q", line)
			continue
		}
		rows++
		toolCell, opCell := strings.TrimSpace(cells[1]), strings.TrimSpace(cells[2])
		var ops []string
		for _, match := range codeSpan.FindAllStringSubmatch(opCell, -1) {
			if !vocabulary.operationIDs[match[1]] {
				t.Errorf("recipe row %q names the unknown operationId %q", cells[0], match[1])
			}
			ops = append(ops, match[1])
		}
		if toolCell == "-" {
			continue
		}
		for _, match := range codeSpan.FindAllStringSubmatch(toolCell, -1) {
			want, ok := operationOf[match[1]]
			if !ok {
				t.Errorf("recipe row %q names the unknown tool %q", cells[0], match[1])
				continue
			}
			if len(ops) == 0 {
				t.Errorf("recipe row %q names tool %q but no operationId", cells[0], match[1])
				continue
			}
			for _, id := range want {
				found := false
				for _, got := range ops {
					found = found || got == id
				}
				// A row that lists several tools lists the union of their operations.
				if !found {
					t.Errorf("recipe row %q: tool %s runs %s, which the row does not list (%v)", cells[0], match[1], id, ops)
				}
			}
		}
	}
	if rows < 25 {
		t.Fatalf("only %d recipe rows were checked; the table format changed", rows)
	}
}

func TestExitCodeTableIsDocumented(t *testing.T) {
	guide := agentguide.Guide()
	for _, row := range []string{"| 0 | Success |", "| 1 | Any other failure", "| 2 | Usage error", "| 3 | Authentication or permission", "| 4 | Not found", "| 5 | Conflict or precondition", "| 6 | Server unreachable"} {
		if !strings.Contains(guide, row) {
			t.Errorf("exit code table is missing the row %q", row)
		}
	}
}

// The appendix draft is not just JSON: it decodes strictly into the contract
// type and passes the same draft validation activation uses.
func TestAppendixDraftIsAValidAnimeDraft(t *testing.T) {
	guide := agentguide.Guide()
	start := strings.Index(guide, "```json\n")
	if start < 0 {
		t.Fatal("the guide has no JSON example")
	}
	block := guide[start+len("```json\n"):]
	block = block[:strings.Index(block, "```")]
	decoder := json.NewDecoder(strings.NewReader(block))
	decoder.DisallowUnknownFields()
	var draft generated.AnimeDraft
	if err := decoder.Decode(&draft); err != nil {
		t.Fatalf("appendix draft does not decode: %v", err)
	}
	mapped, err := animetransport.Draft(draft)
	if err != nil {
		t.Fatalf("appendix draft does not map: %v", err)
	}
	if violations := animedomain.ValidateDraft(mapped); len(violations) > 0 {
		t.Fatalf("appendix draft is invalid: %+v", violations)
	}

	// Preview and activation evaluate only the business snapshot a draft
	// carries, so the appendix on its own cannot be previewed: the guide must
	// route it through load_business_preset, and the loaded draft must satisfy
	// what preview and save require.
	if _, err := animedomain.ResolveDraftSectionsFromBusiness(mapped, nil); err == nil {
		t.Fatal("the appendix draft unexpectedly resolves without a business snapshot; revisit the load step in the guide")
	}
	appendix := guide[strings.Index(guide, "## Appendix"):]
	if !strings.Contains(appendix, "`load_business_preset`") {
		t.Fatal("the appendix must say the draft goes through load_business_preset before preview and save")
	}
	loaded, err := animedomain.LoadBusinessIntoDraft(mapped, guideBusinessPreset(t), json.RawMessage(`{"anibt":{"require":[],"exclude":[]},"title":{"require":[],"exclude":[]}}`))
	if err != nil {
		t.Fatalf("load_business_preset refuses the appendix draft: %v", err)
	}
	if loaded.Business == nil || loaded.BusinessDigest == nil {
		t.Fatal("the loaded appendix draft carries no business snapshot")
	}
	if digest, err := loaded.Business.Digest(); err != nil || digest != *loaded.BusinessDigest {
		t.Fatalf("loaded appendix business digest = %s, %v; draft says %s", digest, err, *loaded.BusinessDigest)
	}
	if _, err := animedomain.ResolveDraftSectionsFromBusiness(loaded, nil); err != nil {
		t.Fatalf("the loaded appendix draft cannot be previewed: %v", err)
	}
	if violations := animedomain.ValidateDraft(loaded); len(violations) > 0 {
		t.Fatalf("the loaded appendix draft is invalid: %+v", violations)
	}
	// The loaded draft still decodes as the public contract type.
	raw, err := json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	strict := json.NewDecoder(bytes.NewReader(raw))
	strict.DisallowUnknownFields()
	var public generated.AnimeDraft
	if err := strict.Decode(&public); err != nil {
		t.Fatalf("the loaded appendix draft does not decode as AnimeDraft: %v", err)
	}
}

// guideBusinessPreset is a complete Global business preset as
// getBusinessPreset returns it on a fresh install (code-default sections).
func guideBusinessPreset(t *testing.T) animedomain.BusinessSnapshot {
	t.Helper()
	preset := animedomain.BusinessSnapshot{SchemaVersion: animedomain.BusinessSnapshotSchemaVersion,
		Sections: map[string]json.RawMessage{}, SectionVersions: map[string]int64{}, SectionSchemas: map[string]string{}}
	for _, name := range animedomain.EffectiveSectionOrder {
		value, err := animedomain.DefaultSectionValue(name)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		preset.Sections[name], preset.SectionVersions[name], preset.SectionSchemas[name] = raw, 1, animedomain.SectionSchemaVersions[name]
	}
	preset.Parser = animedomain.ParserPolicyEnvelope{SchemaVersion: animedomain.ParserPolicyEnvelopeSchemaVersion, Kind: "regex", SourceOwner: "global_parser_policy_default", PolicyVersion: 1}
	preset.Retention = animedomain.RetentionValue{AdmissionMode: "all",
		QualityProfile: json.RawMessage(`{"version":"profile-v1","items":[{"key":"subgroup.score","ranks":[]},{"key":"release.lang","ranks":[["chs"],["sc"],["cht"],["tc"],["jpn"],["eng"]]},{"key":"release.resolution","ranks":[["2160p"],["1440p"],["1080p"],["720p"],["480p"]]},{"key":"release.revision","ranks":[]}]}`)}
	preset.UnclassifiedRoute = "skip"
	preset.Programs = animedomain.ProgramReferences{
		Vars:      namingvars.ProgramRef{Version: namingvars.ProfileVersion("sha256:vars"), Digest: "sha256:vars"},
		AttrRules: namingvars.ProgramRef{Version: attrrules.ProfileVersion("sha256:attr"), Digest: "sha256:attr"},
	}
	return preset
}

func TestInstructionsAreAShortPointerToTheGuide(t *testing.T) {
	text := agentguide.Instructions()
	if len(text) > 1800 || !strings.Contains(text, agentguide.ResourceURI) || !strings.Contains(text, "/api/v1/agent-guide") {
		t.Fatalf("instructions (%d bytes) must stay short and point at the guide", len(text))
	}
	catalog, err := mcp.LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	for _, name := range catalog.ToolNames() {
		known[name] = true
	}
	var unknown []string
	for _, word := range regexp.MustCompile(`[a-z]+(?:_[a-z]+)+`).FindAllString(text, -1) {
		if !known[word] && word != "idempotency_key" && word != "expected_version" && word != agentguide.PromptName && word != "candidate_revision" && word != "current_input" {
			unknown = append(unknown, word)
		}
	}
	sort.Strings(unknown)
	if len(unknown) > 0 {
		t.Fatalf("instructions name things that are not tools: %v", unknown)
	}
}
