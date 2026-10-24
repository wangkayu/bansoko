//go:build linux || darwin

package filesystem

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"

	"bansoko/internal/library/domain"
)

// PreparePublication is a read-only capture: it creates neither a temp file
// nor a target directory. It freezes the source as {path, size}, the parent
// directory identities, the deterministic temp locator and, for overwrite, the
// target as {path, size}. No content is read.
func (executor OSFileExecutor) PreparePublication(ctx context.Context, op domain.FileOperation, permit string) (domain.PublicationPreparation, error) {
	var result domain.PublicationPreparation
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if permit == "" || len(permit) > 128 || !filepath.IsAbs(op.SourcePath) || !filepath.IsAbs(op.TargetPath) || filepath.Clean(op.SourcePath) != op.SourcePath || filepath.Clean(op.TargetPath) != op.TargetPath {
		return result, errors.New("invalid publication preparation identity or path")
	}
	sourceParent, err := openVerifiedDirectory("", filepath.Dir(op.SourcePath), false)
	if err != nil {
		return result, err
	}
	defer sourceParent.Close()
	targetParent, err := openVerifiedDirectory("", filepath.Dir(op.TargetPath), false)
	if err != nil {
		return result, err
	}
	defer targetParent.Close()
	source, err := observeVerifiedEntry(verifiedEntry{parent: sourceParent, name: filepath.Base(op.SourcePath), canonical: op.SourcePath})
	if err != nil {
		return result, sourceOpenError(err)
	}
	if op.ExpectedSizeKnown && source.SizeBytes != op.ExpectedSizeBytes {
		return result, publicationErr(domain.CodeSourceChanged, errors.New("publication source size differs from the frozen size"))
	}
	sum := sha256.Sum256([]byte(permit))
	temp := filepath.Join(filepath.Dir(op.TargetPath), ".bansoko-permit-"+hex.EncodeToString(sum[:]))
	absent, err := entryAbsent(verifiedEntry{parent: targetParent, name: filepath.Base(temp), canonical: temp})
	if err != nil || !absent {
		return result, errors.New("publication temp locator already exists or is unavailable")
	}
	result = domain.PublicationPreparation{Source: source, SourceParent: sourceParent.identity, TargetParent: targetParent.identity, TempPath: temp}
	if op.CollisionPolicy == domain.CollisionOverwrite {
		target, err := observeVerifiedEntry(verifiedEntry{parent: targetParent, name: filepath.Base(op.TargetPath), canonical: op.TargetPath})
		if errors.Is(err, os.ErrNotExist) {
			return result, publicationErr(domain.FileErrorCodeTargetOccupied, errors.New("overwrite target is no longer present"))
		}
		if err != nil {
			return result, publicationErr(domain.CodeTargetUnreadable, err)
		}
		result.ExpectedTarget = &target
	}
	return result, nil
}
