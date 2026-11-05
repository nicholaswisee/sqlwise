package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/gin-gonic/gin"
	"sqlwise/internal/access"
	"sqlwise/internal/auth"
)

type apiErrorEnvelope struct {
	Error apiErrorBody `json:"error"`
}
type apiErrorBody struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
}

func apiData(c *gin.Context, data any) { c.JSON(http.StatusOK, gin.H{"data": data}) }

func apiError(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, apiErrorEnvelope{Error: apiErrorBody{Code: code, Message: message}})
}

func apiFailure(c *gin.Context, err error) {
	switch {
	case errors.Is(err, auth.ErrCSRF):
		apiError(c, http.StatusForbidden, "csrf_invalid", "Request verification failed.")
	case errors.Is(err, auth.ErrSession):
		apiError(c, http.StatusUnauthorized, "session_expired", "Your session has expired.")
	case errors.Is(err, auth.ErrAuthorizationEpoch):
		apiError(c, http.StatusConflict, "authorization_stale", "Your permissions changed. Refresh and try again.")
	case errors.Is(err, access.ErrNotFound), errors.Is(err, auth.ErrUserNotFound):
		apiError(c, http.StatusNotFound, "not_found", "The requested resource was not found.")
	case errors.Is(err, access.ErrConflict), errors.Is(err, auth.ErrUsernameTaken):
		apiError(c, http.StatusConflict, "conflict", "The requested change conflicts with existing data.")
	case errors.Is(err, access.ErrPermissionDenied):
		apiError(c, http.StatusForbidden, "permission_denied", "You do not have permission to perform this action.")
	case errors.Is(err, auth.ErrInvalidInput), errors.Is(err, access.ErrInvalidInput), errors.Is(err, access.ErrInvalidPermission):
		apiError(c, http.StatusUnprocessableEntity, "validation", "Check the submitted values and try again.")
	case errors.Is(err, auth.ErrBusy):
		c.Header("Retry-After", "1")
		apiError(c, http.StatusServiceUnavailable, "busy", "The service is busy. Try again shortly.")
	default:
		apiError(c, http.StatusServiceUnavailable, "unavailable", "The service is temporarily unavailable.")
	}
}

func apiDecode(c *gin.Context, dst any) bool {
	mediaType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || mediaType != "application/json" {
		apiError(c, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json.")
		return false
	}
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			apiError(c, http.StatusRequestEntityTooLarge, "body_too_large", "Request body is too large.")
		} else {
			apiError(c, http.StatusBadRequest, "malformed_json", "Request body must contain one valid JSON value.")
		}
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			apiError(c, http.StatusRequestEntityTooLarge, "body_too_large", "Request body is too large.")
			return false
		}
		apiError(c, http.StatusBadRequest, "malformed_json", "Request body must contain one valid JSON value.")
		return false
	}
	return true
}
