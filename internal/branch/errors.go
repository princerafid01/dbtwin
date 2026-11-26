package branch

import (
	"errors"
	"fmt"
)

// These say what kind of problem stopped a branch command.
// Check them with errors.Is, e.g. errors.Is(err, branch.ErrNotFound).
var (
	ErrInvalidName   = errors.New("invalid branch name")
	ErrMainProtected = errors.New("branch would use the main dataset")
	ErrNotFound      = errors.New("branch not found")
	ErrAlreadyExists = errors.New("branch already exists")
)

// kindError keeps the message we always printed, and also says what kind
// of error it is. The CLI prints the same text as before, and the API can
// use errors.Is to pick the right HTTP status.
type kindError struct {
	msg  string
	kind error
}

func (e kindError) Error() string { return e.msg }
func (e kindError) Unwrap() error { return e.kind }

// newError makes an error of the given kind with a formatted message.
func newError(kind error, format string, args ...any) error {
	return kindError{msg: fmt.Sprintf(format, args...), kind: kind}
}

// notFound is the error for a branch that doesn't exist.
func notFound(branch string) error {
	return newError(ErrNotFound, "branch %s not found (see \"dbtwin list\")", branch)
}
