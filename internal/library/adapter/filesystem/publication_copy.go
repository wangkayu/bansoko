//go:build linux || darwin

package filesystem

import (
	"context"
	"errors"
	"io"
	"time"

	"bansoko/internal/library/domain"
)

const publicationObserverVersion = "local-publication-sync-v1"

// ExecuteHardlink publishes hardlink/block directly: one linkat(source, final)
// after a new publish_target grant. Grant callbacks run before the syscall and
// the syscall returns before the join callback. A link failure is a typed
// HARDLINK_* failure; there is no fallback to copy or move, ever.
func (i *PublicationInvocation) ExecuteHardlink(ctx context.Context, op domain.FileOperation, p domain.PublicationPreparation, callbacks domain.TypedPublicationCallbacks) (resultErr error) {
	if i == nil || !i.consumed.CompareAndSwap(false, true) {
		return errors.New("publication invocation already consumed")
	}
	if callbacks.StartDispatch == nil || callbacks.StartStep == nil || callbacks.RecordStep == nil || callbacks.Joined == nil {
		return errors.New("typed publication callbacks required")
	}
	if op.Operation != domain.OperationHardlink || op.CollisionPolicy != domain.CollisionBlock || op.SourcePath == op.TargetPath {
		return errors.New("typed invocation requires hardlink/block")
	}
	granted, err := callbacks.StartDispatch(ctx, i.session)
	if err != nil {
		return err
	}
	if !granted {
		return errors.New("no new publication dispatch grant")
	}
	covered := []string{}
	defer func() {
		joined := domain.PublicationJoin{Session: i.session, CoveredSteps: covered, JoinedAt: time.Now().UTC(), ObserverVersion: publicationObserverVersion}
		resultErr = errors.Join(resultErr, callbacks.Joined(context.WithoutCancel(ctx), joined))
	}()
	source, err := observePublicationSource(op, p)
	if err != nil {
		return err
	}
	granted, err = callbacks.StartStep(ctx, i.session, "publish_target")
	if err != nil {
		return err
	}
	if !granted {
		return errors.New("no new publication step grant")
	}
	covered = append(covered, "publish_target")
	if callbacks.CheckMutationDeadline != nil {
		if err := callbacks.CheckMutationDeadline(ctx, i.session, "publish_target"); err != nil {
			return err
		}
	}
	mutationErr := i.withEntries(op.SourcePath, op.TargetPath, p.SourceParent, p.TargetParent, func(from, to verifiedEntry) error {
		return i.sys.linkSource(from, to, source)
	})
	receipt := domain.PublicationStepReceipt{Step: "publish_target", Source: source, TargetPath: op.TargetPath, Parent: p.TargetParent, ObservedAt: time.Now().UTC(), Err: mutationErr}
	if mutationErr == nil {
		receipt.Target, receipt.Err = verifyPublishedTarget(op.TargetPath, p.TargetParent, source, true)
		receipt.ObservedAt = time.Now().UTC()
	}
	recordErr := callbacks.RecordStep(context.WithoutCancel(ctx), i.session, receipt)
	return errors.Join(mutationErr, receipt.Err, recordErr)
}

// ExecuteCopy publishes through a sibling temp of the final target: stage the
// temp (copy, or link for a hardlink under version/overwrite), optionally
// reserve a version, publish by rename (absent-target ladder for block,
// rename-replace for version/overwrite), clean up a moved source, then remove
// the temp if it is still present. Each mutation runs only after a new
// committed step grant and every step records a receipt. A failure leaves the
// real prefix for the evidence; nothing is cleaned implicitly.
func (i *PublicationInvocation) ExecuteCopy(ctx context.Context, op domain.FileOperation, p domain.PublicationPreparation, cb domain.TypedPublicationCallbacks) (resultErr error) {
	if i == nil || !i.consumed.CompareAndSwap(false, true) {
		return errors.New("publication invocation already consumed")
	}
	if cb.StartDispatch == nil || cb.StartStep == nil || cb.RecordStep == nil || cb.Joined == nil {
		return errors.New("typed publication callbacks required")
	}
	if (op.Operation != domain.OperationCopy && op.Operation != domain.OperationMove && op.Operation != domain.OperationHardlink) || (op.CollisionPolicy != domain.CollisionBlock && op.CollisionPolicy != domain.CollisionVersion && op.CollisionPolicy != domain.CollisionOverwrite) || op.SourcePath == op.TargetPath {
		return errors.New("typed publication operation or collision invalid")
	}
	if op.CollisionPolicy == domain.CollisionOverwrite && p.ExpectedTarget == nil {
		return errors.New("overwrite requires frozen expected target")
	}
	granted, err := cb.StartDispatch(ctx, i.session)
	if err != nil {
		return err
	}
	if !granted {
		return errors.New("no new dispatch grant")
	}
	covered := []string{}
	defer func() {
		resultErr = errors.Join(resultErr, cb.Joined(context.WithoutCancel(ctx), domain.PublicationJoin{Session: i.session, CoveredSteps: covered, JoinedAt: time.Now().UTC(), ObserverVersion: publicationObserverVersion}))
	}()
	deadline := func(step string) func() error {
		return func() error {
			if cb.CheckMutationDeadline != nil {
				return cb.CheckMutationDeadline(ctx, i.session, step)
			}
			return nil
		}
	}
	grant := func(step string) error {
		ok, err := cb.StartStep(ctx, i.session, step)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("no new step grant")
		}
		covered = append(covered, step)
		return deadline(step)()
	}
	source, err := observePublicationSource(op, p)
	if err != nil {
		return err
	}
	if err := grant("stage_temp"); err != nil {
		return err
	}
	var staged domain.PathObservation
	var stageErr error
	if op.Operation == domain.OperationHardlink {
		stageErr = i.withEntries(op.SourcePath, p.TempPath, p.SourceParent, p.TargetParent, func(from, to verifiedEntry) error {
			return i.sys.linkSource(from, to, source)
		})
		if stageErr == nil {
			staged, stageErr = verifyPublishedTarget(p.TempPath, p.TargetParent, source, true)
		}
	} else {
		staged, stageErr = i.stagePublicationCopy(ctx, op, p, source, deadline("stage_temp"))
	}
	recordErr := cb.RecordStep(context.WithoutCancel(ctx), i.session, domain.PublicationStepReceipt{Step: "stage_temp", Source: source, Target: staged, Parent: p.TargetParent, ObservedAt: time.Now().UTC(), Err: stageErr})
	if stageErr != nil || recordErr != nil {
		return errors.Join(stageErr, recordErr)
	}
	finalPath := op.TargetPath
	var reservation domain.PathObservation
	if op.CollisionPolicy == domain.CollisionVersion {
		if err := grant("reserve_version"); err != nil {
			return err
		}
		var ordinal int64
		finalPath, ordinal, reservation, err = i.reservePublicationVersion(ctx, op, p, deadline("reserve_version"))
		recordErr = cb.RecordStep(context.WithoutCancel(ctx), i.session, domain.PublicationStepReceipt{Step: "reserve_version", Target: reservation, TargetPath: finalPath, CandidateOrdinal: ordinal, Parent: p.TargetParent, ObservedAt: time.Now().UTC(), Err: err})
		if err != nil || recordErr != nil {
			return errors.Join(err, recordErr)
		}
	}
	if err := grant("publish_target"); err != nil {
		return err
	}
	var displaced domain.PathObservation
	publishErr := i.withEntries(p.TempPath, finalPath, p.TargetParent, p.TargetParent, func(temp, final verifiedEntry) error {
		if err := deadline("publish_target")(); err != nil {
			return err
		}
		if i.beforePublish != nil {
			if err := i.beforePublish(p.TempPath, finalPath); err != nil {
				return err
			}
		}
		switch op.CollisionPolicy {
		case domain.CollisionVersion:
			// The reservation was created in this invocation, so its
			// device/inode is comparable.
			var err error
			displaced, err = i.sys.publishReplace(temp, final, staged, reservation, true)
			return err
		case domain.CollisionOverwrite:
			// The frozen target came from preparation: path and size only.
			var err error
			displaced, err = i.sys.publishReplace(temp, final, staged, *p.ExpectedTarget, false)
			return err
		default:
			return i.sys.publishAbsent(temp, final, staged)
		}
	})
	var target domain.PathObservation
	if publishErr == nil {
		target, publishErr = verifyPublishedTarget(finalPath, p.TargetParent, source, op.Operation == domain.OperationHardlink)
	}
	recordErr = cb.RecordStep(context.WithoutCancel(ctx), i.session, domain.PublicationStepReceipt{Step: "publish_target", Source: source, Target: target, TargetPath: finalPath, Displaced: displaced, Parent: p.TargetParent, ObservedAt: time.Now().UTC(), Err: publishErr})
	if publishErr != nil || recordErr != nil {
		return errors.Join(publishErr, recordErr)
	}
	if op.Operation == domain.OperationMove {
		if err := grant("source_cleanup"); err != nil {
			return err
		}
		cleanupErr := i.sys.removePublicationObject(ctx, op.SourcePath, source, true, p.SourceParent, i.cleanupPoint(deadline("source_cleanup")))
		recordErr = cb.RecordStep(context.WithoutCancel(ctx), i.session, domain.PublicationStepReceipt{Step: "source_cleanup", Source: p.Source, Parent: p.SourceParent, ObservedAt: time.Now().UTC(), Err: cleanupErr})
		if cleanupErr != nil || recordErr != nil {
			return errors.Join(cleanupErr, recordErr)
		}
	}
	if err := grant("temp_cleanup"); err != nil {
		return err
	}
	// After a rename the temp is already gone; after the link rung of the
	// absent-target ladder it is still the published object's second name.
	cleanupErr := i.sys.removePublicationObject(ctx, p.TempPath, staged, true, p.TargetParent, i.cleanupPoint(deadline("temp_cleanup")))
	recordErr = cb.RecordStep(context.WithoutCancel(ctx), i.session, domain.PublicationStepReceipt{Step: "temp_cleanup", Source: staged, Parent: p.TargetParent, ObservedAt: time.Now().UTC(), Err: cleanupErr})
	return errors.Join(cleanupErr, recordErr)
}

// cleanupPoint runs the deadline check and the optional fault probe right
// before the cleanup unlink.
func (i *PublicationInvocation) cleanupPoint(deadline func() error) func() error {
	return func() error {
		if err := deadline(); err != nil {
			return err
		}
		if i.cleanupProbe != nil {
			if err := i.cleanupProbe("before_object_purge"); err != nil {
				return err
			}
		}
		return deadline()
	}
}

// withEntries opens the verified parents of two paths (refusing a parent whose
// identity differs from the frozen one) for one mutation.
func (i *PublicationInvocation) withEntries(from, to string, fromParent, toParent domain.FileIdentity, mutate func(verifiedEntry, verifiedEntry) error) error {
	left, right, err := openMutationEntries("", "", from, to, fromParent, toParent)
	if err != nil {
		return err
	}
	defer closeVerifiedEntries(left, right)
	if err := mutate(left, right); err != nil {
		return err
	}
	return syncVerifiedEntries(left, right)
}

// observePublicationSource stats the source through its verified parent: a
// regular file whose size still equals the frozen size.
func observePublicationSource(op domain.FileOperation, p domain.PublicationPreparation) (domain.PathObservation, error) {
	entry, err := openMutationEntry("", op.SourcePath, p.SourceParent)
	if err != nil {
		return domain.PathObservation{}, err
	}
	defer entry.parent.Close()
	source, err := observeVerifiedEntry(entry)
	if err != nil {
		return domain.PathObservation{}, sourceOpenError(err)
	}
	if source.SizeBytes != p.Source.SizeBytes || (op.ExpectedSizeKnown && source.SizeBytes != op.ExpectedSizeBytes) {
		return domain.PathObservation{}, publicationErr(domain.CodeSourceChanged, errors.New("publication source size differs from the frozen size"))
	}
	return source, nil
}

// verifyPublishedTarget stats a published object: a regular file with the
// source size and, for a hardlink, the same device/inode as the source
// observed in this invocation.
func verifyPublishedTarget(path string, parent domain.FileIdentity, source domain.PathObservation, sameObjectRequired bool) (domain.PathObservation, error) {
	entry, err := openMutationEntry("", path, parent)
	if err != nil {
		return domain.PathObservation{}, err
	}
	defer entry.parent.Close()
	target, err := observeVerifiedEntry(entry)
	if err != nil {
		return domain.PathObservation{}, publicationErr(domain.CodeTargetUnreadable, err)
	}
	if target.SizeBytes != source.SizeBytes {
		return target, publicationErr(domain.CodeTargetSizeMismatch, errors.New("published target size differs from the source"))
	}
	if sameObjectRequired && !sameObject(target, source) {
		return target, publicationErr(domain.CodeTargetSizeMismatch, errors.New("published hardlink is not the source object"))
	}
	return target, nil
}

// stagePublicationCopy copies the source into the temp sibling of the target,
// fsyncs the file and its directory, and checks the staged size.
func (i *PublicationInvocation) stagePublicationCopy(ctx context.Context, op domain.FileOperation, p domain.PublicationPreparation, source domain.PathObservation, check func() error) (domain.PathObservation, error) {
	var empty domain.PathObservation
	sourceEntry, err := openMutationEntry("", op.SourcePath, p.SourceParent)
	if err != nil {
		return empty, err
	}
	defer sourceEntry.parent.Close()
	input, err := openEntryNoFollow(sourceEntry)
	if err != nil {
		return empty, sourceOpenError(err)
	}
	defer input.Close()
	opened, err := input.Stat()
	if err != nil {
		return empty, err
	}
	if !opened.Mode().IsRegular() || opened.Size() != source.SizeBytes {
		return empty, publicationErr(domain.CodeSourceChanged, errors.New("opened source differs from the observed source"))
	}
	if err := check(); err != nil {
		return empty, err
	}
	tempEntry, err := openMutationEntry("", p.TempPath, p.TargetParent)
	if err != nil {
		return empty, err
	}
	defer tempEntry.parent.Close()
	output, err := i.sys.create(tempEntry)
	if err != nil {
		return empty, targetMutationError(err)
	}
	// Close is synchronous on every error. No cleanup is performed without a
	// separate temp_cleanup grant, including a partially written temp file.
	copyErr := func() error {
		buffer := make([]byte, 1024*1024)
		var written int64
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := check(); err != nil {
				return err
			}
			n, err := input.Read(buffer)
			if n > 0 {
				w, werr := output.Write(buffer[:n])
				if werr != nil {
					return werr
				}
				if w != n {
					return io.ErrShortWrite
				}
				written += int64(n)
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
		}
		if written != source.SizeBytes {
			return publicationErr(domain.CodeSourceChanged, errors.New("source size changed during copy"))
		}
		if err := check(); err != nil {
			return err
		}
		return errors.Join(output.Sync(), tempEntry.parent.Sync())
	}()
	copyErr = errors.Join(copyErr, output.Close())
	if copyErr != nil {
		return empty, copyErr
	}
	staged, err := observeVerifiedEntry(tempEntry)
	if err != nil {
		return empty, err
	}
	if staged.SizeBytes != source.SizeBytes {
		return empty, publicationErr(domain.CodeTargetSizeMismatch, errors.New("staged size differs from the source"))
	}
	return staged, nil
}
