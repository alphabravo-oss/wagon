// Package httpjson reads size-capped JSON request bodies.
package httpjson

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// Decode reads a JSON body of at most maxBytes into v. It writes 413 for an
// oversized body and 400 for invalid JSON, and reports whether v is usable.
func Decode(w http.ResponseWriter, r *http.Request, v any, maxBytes int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, fmt.Sprintf("Request body is larger than %d bytes", maxBytes), http.StatusRequestEntityTooLarge)
			return false
		}
		http.Error(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}
