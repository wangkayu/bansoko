package domain

import "errors"

// ErrNotRegularFile reports that a path observed as a file holds something
// else (a directory, a symlink, a device). Deletion owners treat it as a
// typed, zero-effect skip.
var ErrNotRegularFile = errors.New("file must be a regular non-symlink file")
