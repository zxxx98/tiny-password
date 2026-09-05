package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/tiny-password/tiny-password/internal/requestid"
)

// writeError emits the stable error envelope bound to the request's
// correlation id. Messages are display-safe: no underlying database, crypto
// or filesystem error text ever reaches the client.
func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	id := requestid.FromContext(r.Context())
	if id == "" {
		id = requestid.New()
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code":       code,
		"message":    message,
		"request_id": id,
	})
}

// maxBodyBytesError reports whether err came from a full request body.
func isBodyTooLarge(err error) bool {
	var maxErr *http.MaxBytesError
	return errors.As(err, &maxErr)
}

// decodeJSONBody decodes a strictly-shaped JSON body of at most maxBytes.
// Oversized bodies map to 413 PAYLOAD_TOO_LARGE (declared size up front, or
// the read limit mid-stream for chunked bodies); malformed or duplicate JSON
// maps to 400 VALIDATION_ERROR. It reports whether decoding succeeded.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, target any, maxBytes int64) bool {
	if r.ContentLength > maxBytes {
		writeError(w, r, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "request body exceeds the allowed size")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		if isBodyTooLarge(err) {
			writeError(w, r, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "request body exceeds the allowed size")
			return false
		}
		writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "invalid request body")
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "invalid request body")
		return false
	}
	return true
}
