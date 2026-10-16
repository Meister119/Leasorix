// Package apperr defines error categories shared by the domain packages. The
// API layer maps them to HTTP status codes.
package apperr

import (
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrNotFound means the referenced object does not exist.
	ErrNotFound = errors.New("not found")
	// ErrConflict means the operation conflicts with existing state
	// (duplicate name, overlapping range, ...).
	ErrConflict = errors.New("conflict")
	// ErrReadOnly means the instance does not accept configuration changes
	// (e.g. HA partner that is not the config leader).
	ErrReadOnly = errors.New("read-only")
)

// FieldError describes a problem with a single input field.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// ValidationError collects one or more field errors.
type ValidationError struct {
	Fields []FieldError
}

func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Fields))
	for i, f := range e.Fields {
		parts[i] = f.Field + ": " + f.Message
	}
	return "validation failed: " + strings.Join(parts, "; ")
}

// Validator accumulates field errors.
type Validator struct {
	fields []FieldError
}

// Add records a field error.
func (v *Validator) Add(field, format string, args ...any) {
	v.fields = append(v.fields, FieldError{Field: field, Message: fmt.Sprintf(format, args...)})
}

// Check records a field error when cond is false.
func (v *Validator) Check(cond bool, field, format string, args ...any) {
	if !cond {
		v.Add(field, format, args...)
	}
}

// Err returns a *ValidationError if any field error was recorded.
func (v *Validator) Err() error {
	if len(v.fields) == 0 {
		return nil
	}
	return &ValidationError{Fields: v.fields}
}

// NotFoundf wraps ErrNotFound with context.
func NotFoundf(format string, args ...any) error {
	return fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), ErrNotFound)
}

// Conflictf wraps ErrConflict with context.
func Conflictf(format string, args ...any) error {
	return fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), ErrConflict)
}
