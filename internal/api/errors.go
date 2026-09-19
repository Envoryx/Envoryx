// Package api implements the versioned HTTP API (/api/v1).
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/envoryx/envoryx/internal/auth"
	"github.com/envoryx/envoryx/internal/disk"
	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/instance"
	"github.com/envoryx/envoryx/internal/project"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// ErrorBody is the uniform error envelope.
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail describes one error.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

// apiError carries an HTTP status with a code.
type apiError struct {
	status  int
	code    string
	message string
	details any
}

func (e *apiError) Error() string { return e.message }

func newError(status int, code, message string) *apiError {
	return &apiError{status: status, code: code, message: message}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Headers are already sent; nothing sensible left to do but log.
		slog.Debug("write json response failed", "err", err)
	}
}

// writeError maps domain errors to HTTP responses.
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	var ae *apiError
	switch {
	case errors.As(err, &ae):
	case errors.Is(err, context.Canceled):
		ae = newError(499, "client_closed", "request cancelled")
	case errors.Is(err, validate.ErrInvalid), errors.Is(err, auth.ErrWeakPassword):
		ae = newError(http.StatusUnprocessableEntity, "validation_failed", err.Error())
	case errors.Is(err, store.ErrNotFound), errors.Is(err, docker.ErrNotFound), errors.Is(err, instance.ErrNotFound):
		ae = newError(http.StatusNotFound, "not_found", "the requested resource does not exist")
	case errors.Is(err, store.ErrConflict), errors.Is(err, instance.ErrPending):
		ae = newError(http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, project.ErrBusy):
		ae = newError(http.StatusConflict, "busy", err.Error())
	case errors.Is(err, project.ErrShuttingDown):
		ae = newError(http.StatusServiceUnavailable, "shutting_down", err.Error())
	case errors.Is(err, project.ErrInterrupted):
		ae = newError(http.StatusServiceUnavailable, "interrupted", err.Error())
	case errors.Is(err, project.ErrOperationTimeout):
		ae = newError(http.StatusGatewayTimeout, "operation_timeout", err.Error())
	case errors.Is(err, docker.ErrNotManaged):
		ae = newError(http.StatusForbidden, "not_managed", "the resource is not managed by Envoryx")
	case errors.Is(err, disk.ErrInsufficient):
		ae = newError(http.StatusInsufficientStorage, "insufficient_storage", err.Error())
	case errors.Is(err, docker.ErrUnavailable):
		ae = newError(http.StatusServiceUnavailable, "docker_unavailable", "the Docker engine is not reachable")
	case errors.Is(err, project.ErrNotConfigured):
		ae = newError(http.StatusServiceUnavailable, "not_configured", err.Error())
	case errors.Is(err, auth.ErrInvalidCredentials):
		ae = newError(http.StatusUnauthorized, "invalid_credentials", "invalid username or password")
	case errors.Is(err, auth.ErrTooManyAttempts):
		ae = newError(http.StatusTooManyRequests, "rate_limited", err.Error())
	case errors.Is(err, auth.ErrUnauthenticated):
		ae = newError(http.StatusUnauthorized, "unauthenticated", "authentication required")
	default:
		slog.Error("unhandled error", "method", r.Method, "path", r.URL.Path, "err", err)
		ae = newError(http.StatusInternalServerError, "internal_error", "an internal error occurred")
		if err != nil {
			// Operators need the cause; the message is generic for the browser but the
			// detail carries the wrapped chain (no secrets are ever put into errors).
			ae.details = map[string]string{"cause": err.Error()}
		}
	}
	writeJSON(w, ae.status, ErrorBody{Error: ErrorDetail{Code: ae.code, Message: ae.message, Details: ae.details}})
}

// unauthorized is the handler used by the auth middleware.
func unauthorized(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, auth.ErrUnauthenticated)
}

const maxBodyBytes = 1 << 20

// decodeJSON reads a JSON body with a size limit and rejects unknown fields.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	if r.Body == nil {
		return newError(http.StatusBadRequest, "bad_request", "request body required")
	}
	body := http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return newError(http.StatusRequestEntityTooLarge, "too_large", "request body too large")
		}
		return newError(http.StatusBadRequest, "bad_request", fmt.Sprintf("invalid JSON body: %v", err))
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return newError(http.StatusBadRequest, "bad_request", "unexpected data after JSON body")
	}
	return nil
}
