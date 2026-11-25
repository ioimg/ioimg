package server

import (
	"errors"
	"fmt"
	"net/http"
)

// HTTPError is the public error model from docs/API.md §6.
type HTTPError struct {
	Status    int
	Code      string
	Message   string
	Retryable bool
}

func (e *HTTPError) Error() string { return e.Code + ": " + e.Message }

func errInvalidOps(msg string) *HTTPError {
	return &HTTPError{Status: http.StatusBadRequest, Code: "invalid_ops", Message: msg}
}

func errInvalidSource() *HTTPError {
	return &HTTPError{Status: http.StatusBadRequest, Code: "invalid_source_url", Message: "source must be an absolute http(s) URL"}
}

func errProjectNotFound() *HTTPError {
	return &HTTPError{Status: http.StatusNotFound, Code: "project_not_found", Message: "no project is configured for this host"}
}

func errInvalidSignature() *HTTPError {
	return &HTTPError{Status: http.StatusUnauthorized, Code: "invalid_signature", Message: "signature is invalid or missing"}
}

func errSignatureExpired() *HTTPError {
	return &HTTPError{Status: http.StatusGone, Code: "signature_expired", Message: "signed URL has expired"}
}

func errNotAllowed(host string) *HTTPError {
	return &HTTPError{Status: http.StatusForbidden, Code: "source_not_allowed", Message: fmt.Sprintf("host %q is not in the allowlist", host)}
}

func errPrivate() *HTTPError {
	return &HTTPError{Status: http.StatusForbidden, Code: "private_address_blocked", Message: "source resolves to a private or metadata address"}
}

func errNotFound() *HTTPError {
	return &HTTPError{Status: http.StatusNotFound, Code: "source_not_found", Message: "upstream returned 404"}
}

func errTooLarge() *HTTPError {
	return &HTTPError{Status: http.StatusRequestEntityTooLarge, Code: "payload_too_large", Message: "source exceeds the configured size limit"}
}

func errDecode() *HTTPError {
	return &HTTPError{Status: http.StatusUnprocessableEntity, Code: "decode_failed", Message: "source is not a supported or valid image"}
}

func errHashsumMismatch() *HTTPError {
	return &HTTPError{Status: http.StatusUnprocessableEntity, Code: "hashsum_mismatch", Message: "source hashsum does not match hs_"}
}

func errDimension() *HTTPError {
	return &HTTPError{Status: http.StatusUnprocessableEntity, Code: "dimension_limit_exceeded", Message: "image exceeds the 40 MP / 8192 px limit"}
}

func errOrigin(msg string) *HTTPError {
	return &HTTPError{Status: http.StatusBadGateway, Code: "origin_error", Message: msg, Retryable: true}
}

func errTimeout() *HTTPError {
	return &HTTPError{Status: http.StatusGatewayTimeout, Code: "origin_timeout", Message: "upstream fetch timed out", Retryable: true}
}

func errOverloaded() *HTTPError {
	return &HTTPError{Status: http.StatusServiceUnavailable, Code: "overloaded", Message: "worker pool is saturated", Retryable: true}
}

func errUnsupportedFormat(format string) *HTTPError {
	return &HTTPError{Status: http.StatusUnsupportedMediaType, Code: "unsupported_format", Message: "output format is disabled: " + format}
}

func errQuotaExceeded() *HTTPError {
	return &HTTPError{Status: http.StatusTooManyRequests, Code: "quota_exceeded", Message: "monthly transform quota exhausted", Retryable: false}
}

func errRateLimited() *HTTPError {
	return &HTTPError{Status: http.StatusTooManyRequests, Code: "rate_limited", Message: "too many requests", Retryable: true}
}

func errInternal(err error) *HTTPError {
	return &HTTPError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "internal error", Retryable: true}
}

// asHTTPError converts any pipeline error into the public model.
func asHTTPError(err error) *HTTPError {
	var he *HTTPError
	if errors.As(err, &he) {
		return he
	}
	return errInternal(err)
}
