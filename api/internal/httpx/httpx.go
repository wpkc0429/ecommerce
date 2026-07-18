// Package httpx defines the unified HTTP response envelope of the API.
//
// Error semantics follow design D12:
//
//	401 unauthenticated / token audience mismatch
//	403 permission denied / cross-shop access
//	404 unbound domain, missing page, shop under review
//	422 schema validation failure (details carry JSON Pointer locations)
//	503 shop disabled (storefront renders a maintenance page)
//
// Every error body is {"error": {"code", "message", "details"}}.
package httpx

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// ErrorDetail is the inner payload of the unified error envelope.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

// ErrorBody is the unified error envelope (design D12).
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ValidationDetail locates one schema violation via JSON Pointer.
type ValidationDetail struct {
	Pointer string `json:"pointer"`
	Message string `json:"message"`
}

// WriteJSON writes v as a JSON response with the given status code.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("httpx: encode response", "err", err)
	}
}

// WriteError writes the unified error envelope.
func WriteError(w http.ResponseWriter, status int, code, message string, details any) {
	WriteJSON(w, status, ErrorBody{Error: ErrorDetail{Code: code, Message: message, Details: details}})
}

func BadRequest(w http.ResponseWriter, message string) {
	WriteError(w, http.StatusBadRequest, "bad_request", message, nil)
}

func Unauthorized(w http.ResponseWriter) {
	WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required or credentials invalid", nil)
}

func Forbidden(w http.ResponseWriter) {
	WriteError(w, http.StatusForbidden, "forbidden", "permission denied", nil)
}

func NotFound(w http.ResponseWriter) {
	WriteError(w, http.StatusNotFound, "not_found", "resource not found", nil)
}

func Conflict(w http.ResponseWriter, message string) {
	WriteError(w, http.StatusConflict, "conflict", message, nil)
}

// Unprocessable reports schema/constraint validation failures (422).
func Unprocessable(w http.ResponseWriter, message string, details any) {
	WriteError(w, http.StatusUnprocessableEntity, "validation_failed", message, details)
}

// ShopDisabled reports a disabled shop (503); the SSR renders a maintenance page.
func ShopDisabled(w http.ResponseWriter) {
	WriteError(w, http.StatusServiceUnavailable, "shop_disabled", "shop is temporarily unavailable", nil)
}

func Internal(w http.ResponseWriter) {
	WriteError(w, http.StatusInternalServerError, "internal", "internal server error", nil)
}
