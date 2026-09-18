package middleware

import (
	"encoding/json"
	"net/http"
)

// WriteError writes the common JSON error envelope with the stable code and
// the request correlation identity.
func WriteError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorEnvelope{Error: ErrorBody{
		Code:          code,
		Message:       message,
		CorrelationID: CorrelationFrom(r.Context()),
	}})
}

// WriteJSON writes one JSON response body.
func WriteJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
