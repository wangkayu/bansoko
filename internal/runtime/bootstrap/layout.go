package bootstrap

import (
	"errors"
	"fmt"

	"bansoko/internal/update/packagemaker"
)

// Unified releases carry one executable. The retired standalone migrate member
// is rejected explicitly so an old release cannot be mistaken for the new form.
func validateUnifiedReleaseMetadata(meta packagemaker.Metadata) error {
	server := false
	for _, file := range meta.Files {
		switch file.Path {
		case packagemaker.ServerEntryPath:
			server = true
		case packagemaker.MigrateEntryPath:
			return errors.New("legacy server+migrate release layout is retired")
		}
	}
	if !server {
		return errors.New("verified release has no server binary")
	}
	return nil
}

type processCommand struct {
	path string
	args []string
}

func (c processCommand) argv() []string {
	argv := make([]string, 0, len(c.args)+1)
	argv = append(argv, c.path)
	return append(argv, c.args...)
}

func commandFor(path, kind string) processCommand {
	mode := kind
	if kind == "server" {
		mode = "serve"
	}
	return processCommand{path: path, args: []string{mode}}
}

// verifiedReleaseCommand verifies the signed file manifest before selecting
// the single executable and its explicit mode.
func verifiedReleaseCommand(dir, kind string) (processCommand, error) {
	if kind != "server" && kind != "migrate" {
		return processCommand{}, fmt.Errorf("unsupported release command kind %q", kind)
	}
	meta, err := verifyReleaseTreeMetadata(dir)
	if err != nil {
		return processCommand{}, err
	}
	if err := validateUnifiedReleaseMetadata(meta); err != nil {
		return processCommand{}, err
	}
	return commandFor(releaseServerBin(dir), kind), nil
}
