// Package httpapi holds the JSON conventions shared by every HTTP handler.
package httpapi

import (
	"encoding/json"
	"errors"
	"mime"
	"net/http"
)

// errorBody is the one shape every error response uses:
//
//	{"error": {"code": "invalid_credentials", "message": "..."}}
//
// Code is stable and meant for programs; Message is meant for people.
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// WriteJSON writes v as a JSON response.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError writes an error response in the shared shape.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	WriteJSON(w, status, errorBody{Error: errorDetail{Code: code, Message: message}})
}

// ErrNotJSON means the request did not declare a JSON body.
var ErrNotJSON = errors.New("content type must be application/json")

// DecodeJSON reads a JSON request body of at most maxBytes into dst.
//
// It requires Content-Type: application/json. Browsers cannot send that type
// cross-site from a plain form, which keeps other sites from posting to
// cookie-authenticated endpoints.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return ErrNotJSON
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	return json.NewDecoder(r.Body).Decode(dst)
}
