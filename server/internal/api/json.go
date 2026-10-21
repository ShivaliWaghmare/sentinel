package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// maxBodyBytes caps request bodies; every JSON body we accept is small.
const maxBodyBytes = 1 << 20 // 1 MB

// errorResponse is the body of every error: {"error": "message"}.
type errorResponse struct {
	Error string `json:"error"`
}

// requestError is a client mistake in the request body, with the HTTP status
// to report and a message that is safe to show.
type requestError struct {
	status int
	msg    string
}

func (e *requestError) Error() string { return e.msg }

// decodeJSON reads one JSON object from the body into dst. It rejects unknown
// fields, trailing data and bodies over maxBodyBytes. Returned errors are
// *requestError with a readable message.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		return &requestError{status: statusForDecodeError(err), msg: decodeErrorMessage(err)}
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return &requestError{status: http.StatusBadRequest, msg: "request body must contain a single JSON object"}
	}
	return nil
}

func statusForDecodeError(err error) int {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusBadRequest
}

func decodeErrorMessage(err error) string {
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	var maxErr *http.MaxBytesError
	switch {
	case errors.Is(err, io.EOF):
		return "request body must not be empty"
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "request body contains malformed JSON"
	case errors.As(err, &syntaxErr):
		return fmt.Sprintf("request body contains malformed JSON (at byte %d)", syntaxErr.Offset)
	case errors.As(err, &typeErr):
		if typeErr.Field != "" {
			return fmt.Sprintf("field %q must be of type %s", typeErr.Field, typeErr.Type)
		}
		return "request body must be a JSON object"
	case errors.As(err, &maxErr):
		return fmt.Sprintf("request body must not be larger than %d bytes", maxErr.Limit)
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		// encoding/json has no typed error for unknown fields.
		return "unknown field " + strings.TrimPrefix(err.Error(), "json: unknown field ")
	default:
		return "invalid request body"
	}
}

// writeJSON writes v as JSON with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Headers are already sent, so all we can do is log.
		slog.Error("encode response", "err", err)
	}
}

// writeError writes {"error": msg} with the given status.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorResponse{Error: msg})
}

// writeRequestError reports a decodeJSON error.
func writeRequestError(w http.ResponseWriter, err error) {
	var reqErr *requestError
	if errors.As(err, &reqErr) {
		writeError(w, reqErr.status, reqErr.msg)
		return
	}
	writeError(w, http.StatusBadRequest, "invalid request body")
}

// formatTime renders timestamps as RFC 3339 in UTC with milliseconds.
func formatTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}
