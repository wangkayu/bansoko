package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"bansoko/internal/agentguide"
)

// cmdGuide prints the agent guide embedded in this binary. The text is the
// same file the server serves at GET /api/v1/agent-guide and over MCP, so no
// server or credential is needed. --skill wraps it as a SKILL.md document.
func cmdGuide(args []string) error {
	fs := flag.NewFlagSet("guide", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asSkill := fs.Bool("skill", false, "print the guide as a SKILL.md document with front matter")
	name := fs.String("name", agentguide.SkillName, "skill name used in the SKILL.md front matter (with --skill)")
	if err := fs.Parse(args); err != nil {
		return parseError(err)
	}
	if fs.NArg() > 0 {
		return usageErrorf("guide takes no positional arguments, got %q", strings.Join(fs.Args(), " "))
	}
	if !*asSkill && flagWasSet(fs, "name") {
		return newUsageError("--name only applies with --skill")
	}
	format, body := "guide", agentguide.Guide()
	if *asSkill {
		format, body = "skill", agentguide.Skill(*name)
	}
	if jsonMode {
		emitJSON(map[string]any{"format": format, "content": body})
		return nil
	}
	fmt.Fprint(os.Stdout, body)
	return nil
}
