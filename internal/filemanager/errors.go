package filemanager

import "errors"

// ErrorKind is the transport-neutral class of a typed browse error.
type ErrorKind int

const (
	ErrorInvalid ErrorKind = iota + 1
	ErrorNotFound
	ErrorConflict
	// ErrorUnavailable: a provider the request reads could not be reached.
	ErrorUnavailable
	// ErrorForbidden: the path is well-formed and exists, but lies outside the
	// library roots the owner added.
	ErrorForbidden
)

// Typed browse error codes. Each is stable and carries no private path.
const (
	CodePathInvalid        = "FILE_MANAGER_PATH_INVALID"
	CodeCursorInvalid      = "FILE_MANAGER_CURSOR_INVALID"
	CodeSymlinkNotFollowed = "FILE_MANAGER_SYMLINK_NOT_FOLLOWED"
	CodeNotADirectory      = "FILE_MANAGER_NOT_A_DIRECTORY"
	CodePathNotFound       = "FILE_MANAGER_PATH_NOT_FOUND"
	CodeLocationUnknown    = "FILE_MANAGER_LOCATION_UNKNOWN"
	CodePathUnreadable     = "FILE_MANAGER_PATH_UNREADABLE"
	// CodePathDenied: the path reaches an application-data or system
	// directory the browser refuses to read or write, whatever the container
	// user's own permissions allow.
	CodePathDenied = "FILE_MANAGER_PATH_DENIED"
	// CodePathOutsideRoots: the path lies outside every library directory the
	// owner added, so it is neither browsed nor accepted as a target.
	CodePathOutsideRoots = "FILE_MANAGER_PATH_OUTSIDE_ROOTS"
	// CodeEntryLocked: the operation would change a managed file or a subtree
	// holding one; the detail names the locked paths.
	CodeEntryLocked = "FILE_MANAGER_ENTRY_LOCKED"
	// Root management: adding a root refuses a path that is not an existing
	// directory reached without a symbolic link, the filesystem root itself,
	// or a directory that holds an application-data directory.
	CodeRootInvalid  = "FILE_MANAGER_ROOT_INVALID"
	CodeRootExists   = "FILE_MANAGER_ROOT_EXISTS"
	CodeRootNotFound = "FILE_MANAGER_ROOT_NOT_FOUND"
)

// Store-level root conflicts, translated by the service into typed errors.
var (
	ErrRootExists   = errors.New("file manager root already exists")
	ErrRootNotFound = errors.New("file manager root does not exist")
)

// Error is a typed browse error. Paths carries the paths a refusal is about
// (the managed files inside a refused directory, for example).
type Error struct {
	Kind    ErrorKind
	Code    string
	Message string
	Paths   []string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func invalid(code, message string) *Error {
	return &Error{Kind: ErrorInvalid, Code: code, Message: message}
}

func notFound(code, message string) *Error {
	return &Error{Kind: ErrorNotFound, Code: code, Message: message}
}

func conflict(code, message string) *Error {
	return &Error{Kind: ErrorConflict, Code: code, Message: message}
}

func forbidden(code, message string) *Error {
	return &Error{Kind: ErrorForbidden, Code: code, Message: message}
}
