package apierror

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/swaggest/usecase/status"
)

func TestMakeResponseHidesDockerDetails(t *testing.T) {
	ctx := context.WithValue(context.Background(), middleware.RequestIDKey, "request-123")
	err := errors.New("Error response from daemon: dial unix /var/run/docker.sock: connect: permission denied")
	httpStatus, body := makeResponse(ctx, status.Wrap(err, status.FailedPrecondition))
	response := body.(Response)

	if httpStatus != http.StatusPreconditionFailed {
		t.Fatalf("expected 412, got %d", httpStatus)
	}
	if strings.Contains(response.Error, "docker.sock") || strings.Contains(response.Error, "daemon") {
		t.Fatalf("technical detail leaked: %q", response.Error)
	}
	if response.ErrorCode != "failed_precondition" || response.RequestID != "request-123" {
		t.Fatalf("unexpected response: %#v", response)
	}
}

func TestMakeResponseKeepsSafeValidationMessage(t *testing.T) {
	_, body := makeResponse(context.Background(), status.Wrap(errors.New("container_id is required"), status.InvalidArgument))
	response := body.(Response)

	if response.Error != "Container_id is required." || response.ErrorCode != "invalid_request" {
		t.Fatalf("unexpected response: %#v", response)
	}
}
