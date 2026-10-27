package domain

import "errors"

// ErrNotRegularFile reports that a path observed as a file holds something
// else (a directory, a symlink, a device). Deletion owners treat it as a
// typed, zero-effect skip.
var ErrNotRegularFile = errors.New("file must be a regular non-symlink file")

// ErrFileBusy reports that the rename or unlink syscall of a file effect was
// refused because the file is held open elsewhere (a player reading it over
// SMB/CIFS answers with a sharing violation) or because the effect was not
// permitted. The filesystem adapter wraps only the effect syscall itself, so
// the error proves the call returned without completing; whether the effect
// happened is still decided by observation. Owners retry it on a bounded
// schedule and report it as "busy or not permitted" when the schedule ends.
var ErrFileBusy = errors.New("file is busy or the change is not permitted")

// FileBusy reports whether err carries ErrFileBusy.
func FileBusy(err error) bool {
	return err != nil && errors.Is(err, ErrFileBusy)
}

// ErrFileReadOnly reports that the rename or unlink syscall of a file effect
// was refused by a read-only filesystem (EROFS). Like ErrFileBusy it proves
// only that the call returned without completing; unlike it, the refusal is
// not retried: waiting does not make a read-only mount writable.
var ErrFileReadOnly = errors.New("the file is on a read-only filesystem")

// FileReadOnly reports whether err carries ErrFileReadOnly.
func FileReadOnly(err error) bool {
	return err != nil && errors.Is(err, ErrFileReadOnly)
}
