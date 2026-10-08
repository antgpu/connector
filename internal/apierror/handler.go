package apierror

import (
	"context"
	"errors"
	"log"
	"strings"
	"unicode"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/swaggest/rest"
	swaggestnethttp "github.com/swaggest/rest/nethttp"
	"github.com/swaggest/usecase"
	"github.com/swaggest/usecase/status"
)

// Response is the stable, user-safe error shape returned by API handlers.
type Response struct {
	Status    string `json:"status,omitempty" description:"Canonical error status."`
	Error     string `json:"error" description:"Human-readable error message."`
	ErrorCode string `json:"error_code" description:"Stable application error code."`
	RequestID string `json:"request_id,omitempty" description:"Request reference for support."`
}

// NewHandler applies the public error contract to every Swaggest handler.
func NewHandler(interactor usecase.Interactor, options ...func(*swaggestnethttp.Handler)) *swaggestnethttp.Handler {
	options = append(options, func(handler *swaggestnethttp.Handler) {
		handler.MakeErrResp = makeResponse
	})
	return swaggestnethttp.NewHandler(interactor, options...)
}

func makeResponse(ctx context.Context, err error) (int, interface{}) {
	httpStatus, original := rest.Err(err)
	code := canonicalCode(err)
	requestID := middleware.GetReqID(ctx)
	message, errorCode, sensitive := publicError(code, err)

	if requestID != "" && (sensitive || code == status.Internal || code == status.Unknown || code == status.DataLoss) {
		log.Printf("request_id=%s api_error=%v", requestID, err)
	}

	return httpStatus, Response{
		Status:    original.StatusText,
		Error:     message,
		ErrorCode: errorCode,
		RequestID: requestID,
	}
}

func canonicalCode(err error) status.Code {
	var withStatus rest.ErrWithCanonicalStatus
	if errors.As(err, &withStatus) {
		return withStatus.Status()
	}
	return status.Unknown
}

func publicError(code status.Code, err error) (message string, errorCode string, sensitive bool) {
	message, errorCode = defaultError(code)

	if code == status.Internal || code == status.Unknown || code == status.DataLoss || code == status.Unavailable || code == status.DeadlineExceeded {
		return message, errorCode, true
	}

	cause := err
	if unwrapped := errors.Unwrap(err); unwrapped != nil {
		cause = unwrapped
	}
	candidate := stripStatusPrefix(strings.TrimSpace(cause.Error()))
	if candidate == "" || containsTechnicalDetail(candidate) {
		return message, errorCode, candidate != ""
	}

	return sentence(candidate), errorCode, false
}

func defaultError(code status.Code) (string, string) {
	switch code {
	case status.Canceled:
		return "The request was canceled. Please try again.", "request_canceled"
	case status.InvalidArgument:
		return "The request could not be processed. Check the provided information and try again.", "invalid_request"
	case status.DeadlineExceeded:
		return "The request timed out. Please try again.", "request_timeout"
	case status.NotFound:
		return "The requested resource was not found.", "not_found"
	case status.AlreadyExists:
		return "The requested resource already exists.", "already_exists"
	case status.PermissionDenied:
		return "You do not have permission to perform this action.", "permission_denied"
	case status.ResourceExhausted:
		return "The request limit has been reached. Please try again later.", "resource_exhausted"
	case status.FailedPrecondition:
		return "This action cannot be completed in the resource's current state.", "failed_precondition"
	case status.Aborted:
		return "The operation could not be completed. Please try again.", "operation_aborted"
	case status.OutOfRange:
		return "A provided value is outside the allowed range.", "out_of_range"
	case status.Unimplemented:
		return "This operation is not supported.", "not_supported"
	case status.Unavailable:
		return "The service is temporarily unavailable. Please try again.", "service_unavailable"
	case status.Unauthenticated:
		return "Authentication is required.", "authentication_required"
	default:
		return "Something went wrong. Please try again. If the problem continues, contact support.", "internal_error"
	}
}

func stripStatusPrefix(message string) string {
	for _, prefix := range []string{
		"canceled: ", "unknown: ", "invalid argument: ", "deadline exceeded: ",
		"not found: ", "already exists: ", "permission denied: ", "resource exhausted: ",
		"failed precondition: ", "aborted: ", "out of range: ", "unimplemented: ",
		"internal: ", "unavailable: ", "data loss: ", "unauthenticated: ",
	} {
		if strings.HasPrefix(strings.ToLower(message), prefix) {
			return strings.TrimSpace(message[len(prefix):])
		}
	}
	return message
}

func containsTechnicalDetail(message string) bool {
	lower := strings.ToLower(message)
	for _, marker := range []string{
		"sql:", "pq:", "pgx:", "scan error", "driver:", "dial tcp", "connection refused",
		"broken pipe", "runtime error:", "panic:", "error response from daemon", "docker:",
		"containerd:", "unix://", "/var/run/", "exec:",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func sentence(message string) string {
	runes := []rune(strings.TrimSpace(message))
	if len(runes) == 0 {
		return message
	}
	runes[0] = unicode.ToUpper(runes[0])
	message = string(runes)
	if !strings.ContainsAny(message[len(message)-1:], ".?!") {
		message += "."
	}
	return message
}
