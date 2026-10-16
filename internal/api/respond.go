package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/meister119/leasorix/internal/api/gen"
	"github.com/meister119/leasorix/internal/apperr"
	"github.com/meister119/leasorix/internal/auth"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string, fields []gen.FieldError) {
	e := gen.Error{Error: msg}
	if len(fields) > 0 {
		e.Fields = &fields
	}
	writeJSON(w, status, e)
}

// fail maps domain errors to HTTP responses.
func fail(log *slog.Logger, w http.ResponseWriter, r *http.Request, err error) {
	var ve *apperr.ValidationError
	switch {
	case errors.As(err, &ve):
		fields := make([]gen.FieldError, len(ve.Fields))
		for i, f := range ve.Fields {
			fields[i] = gen.FieldError{Field: f.Field, Message: f.Message}
		}
		writeError(w, http.StatusBadRequest, "validation failed", fields)
	case errors.Is(err, apperr.ErrNotFound):
		writeError(w, http.StatusNotFound, trimSentinel(err, apperr.ErrNotFound), nil)
	case errors.Is(err, apperr.ErrConflict):
		writeError(w, http.StatusConflict, trimSentinel(err, apperr.ErrConflict), nil)
	case errors.Is(err, apperr.ErrReadOnly):
		writeError(w, http.StatusConflict, err.Error(), nil)
	case errors.Is(err, auth.ErrForbidden):
		writeError(w, http.StatusForbidden, trimSentinel(err, auth.ErrForbidden), nil)
	case errors.Is(err, auth.ErrInvalidCredentials):
		writeError(w, http.StatusUnauthorized, err.Error(), nil)
	default:
		log.Error("api request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		writeError(w, http.StatusInternalServerError, "internal error", nil)
	}
}

func trimSentinel(err, sentinel error) string {
	return strings.TrimSuffix(err.Error(), ": "+sentinel.Error())
}

// decode parses a JSON request body strictly. It writes the error response
// and returns false on failure.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large", nil)
			return false
		}
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON body: %v", err), nil)
		return false
	}
	return true
}

func badRequest(w http.ResponseWriter, field, msg string) {
	writeError(w, http.StatusBadRequest, "validation failed", []gen.FieldError{{Field: field, Message: msg}})
}
