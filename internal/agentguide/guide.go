// Package agentguide owns the English operating guide for external agents and
// workflows. The guide is one Markdown file embedded in the binary, so it
// always matches the running version. Every agent-facing surface serves this
// same text: the MCP instructions, resource and prompt, GET /api/v1/agent-guide,
// `bansoko guide`, and the generated repository skills.
package agentguide

import (
	_ "embed"
	"strings"
)

//go:embed guide.md
var guide string

const (
	// ResourceURI is the MCP resource that serves the full guide.
	ResourceURI = "bansoko://agent-guide"
	// PromptName is the MCP prompt that returns the full guide.
	PromptName = "agent_guide"
	// SkillName is the default skill name used by the HTTP and CLI exports.
	SkillName = "bansoko-agent"

	skillDescription = "Operate a Bansoko instance (anime subscriptions, one-shot acquisitions, " +
		"file matching, execution records) through its MCP server, HTTP API or bansoko CLI. " +
		"Use when asked to subscribe to, inspect, retry or repair anime downloads in Bansoko."
)

// Guide returns the full guide.
func Guide() string { return guide }

// Skill returns the guide wrapped as a SKILL.md document with name and
// description front matter.
func Skill(name string) string {
	if strings.TrimSpace(name) == "" {
		name = SkillName
	}
	var b strings.Builder
	b.WriteString("---\nname: ")
	b.WriteString(name)
	b.WriteString("\ndescription: ")
	b.WriteString(skillDescription)
	b.WriteString("\n---\n\n")
	b.WriteString(guide)
	return b.String()
}

// Instructions is the short summary sent as the MCP server instructions. It
// points at the full guide instead of repeating it.
func Instructions() string {
	return "Bansoko operates anime subscriptions and one-shot downloads. " +
		"Read the full guide first: MCP resource " + ResourceURI + " (or prompt " + PromptName + "), " +
		"GET /api/v1/agent-guide, or `bansoko guide`. " +
		"Core flow: search_anime, discover_feed_groups, refresh_feed_snapshot, then create_candidate, rss_list_view(candidate_revision), activate_candidate(revision), refresh_rss, poll_workspace. " +
		"One-shot flow: refresh_feed_snapshot, rss_list_view(current_input), acquire_seed_once with a seed reference (snapshot identity plus the seed key, never a URL), get_one_shot_acquisition. " +
		"Writes use compare-and-swap versions (expected_version) read from the owner first; idempotency keys are generated unless you pass idempotency_key. " +
		"Repair is re-enqueue (retry_release, retry_import, refresh_rss); a one-shot that failed before import is admitted again as a new one-shot. Never edit the database by hand. " +
		"Delete and retire tools are destructive: confirm with the user first. " +
		"Download URLs and passkeys are never returned and must not be requested."
}
