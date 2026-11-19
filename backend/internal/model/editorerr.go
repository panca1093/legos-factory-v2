package model

import (
	"errors"
	"fmt"
)

var (
	ErrBadPath              = errors.New("invalid path")
	ErrEditorFolderNotFound = errors.New("folder not found")
)

// SizedError carries the byte size of a file refused as too large or binary.
type SizedError struct {
	Err  error
	Size int64
}

func (e *SizedError) Error() string { return fmt.Sprintf("%s: %d bytes", e.Err, e.Size) }
func (e *SizedError) Unwrap() error { return e.Err }
