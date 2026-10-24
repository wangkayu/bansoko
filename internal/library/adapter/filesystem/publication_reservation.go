//go:build linux || darwin

package filesystem

import (
	"context"
	"errors"
	"os"

	"bansoko/internal/library/domain"
)

// reservePublicationVersion is called only inside a newly granted
// reserve_version step. Each candidate is reserved with O_CREAT|O_EXCL, the
// only atomic no-replace create that works on NFS and SMB. Failed exclusive
// creates have no object to clean; once a creation succeeds no next candidate
// is tried, even if flush or observation fails.
func (i *PublicationInvocation) reservePublicationVersion(ctx context.Context, op domain.FileOperation, p domain.PublicationPreparation, checks ...func() error) (string, int64, domain.PathObservation, error) {
	var empty domain.PathObservation
	for ordinal := int64(1); ordinal <= 1001; ordinal++ {
		if err := ctx.Err(); err != nil {
			return "", 0, empty, err
		}
		for _, check := range checks {
			if err := check(); err != nil {
				return "", 0, empty, err
			}
		}
		candidate := op.TargetPath
		if ordinal > 1 {
			candidate = versionedPath(op.TargetPath, int(ordinal))
		}
		entry, err := openMutationEntry("", candidate, p.TargetParent)
		if err != nil {
			return candidate, ordinal, empty, err
		}
		file, err := i.sys.create(entry)
		if errors.Is(err, os.ErrExist) {
			_ = entry.parent.Close()
			continue
		}
		if err != nil {
			_ = entry.parent.Close()
			return candidate, ordinal, empty, targetMutationError(err)
		}
		err = errors.Join(file.Sync(), file.Close(), entry.parent.Sync())
		if err != nil {
			_ = entry.parent.Close()
			return candidate, ordinal, empty, err
		}
		observation, err := observeVerifiedEntry(entry)
		_ = entry.parent.Close()
		if err == nil && observation.SizeBytes != 0 {
			err = errors.New("version reservation is not empty")
		}
		return candidate, ordinal, observation, err
	}
	return "", 0, empty, errors.New("version reservation limit exhausted")
}
