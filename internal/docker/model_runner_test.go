package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestModelRunnerBaseURLsFromEnv(t *testing.T) {
	t.Setenv(modelRunnerBaseEnv, "http://localhost:22434")
	t.Setenv(modelRunnerBasesEnv, "http://127.0.0.1:12434, http://localhost:22434")

	got := modelRunnerBaseURLsFromEnv()
	if len(got) < 3 {
		t.Fatalf("expected at least 3 URLs, got %v", got)
	}
	if got[0] != "http://127.0.0.1:12434" {
		t.Fatalf("unexpected first URL: %q", got[0])
	}
	if got[1] != "http://localhost:22434" {
		t.Fatalf("unexpected second URL: %q", got[1])
	}
	if got[len(got)-1] != defaultModelRunnerBaseURL {
		t.Fatalf("expected default URL in list, got %v", got)
	}
}

func TestModelRunnerServiceFlow(t *testing.T) {
	const model = "ai/smollm2"

	var pullSeen bool
	var deleteSeen bool

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/engines/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"ai/smollm2"}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"name":"ai/smollm2"}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/models/create":
			var payload map[string]string
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode pull payload: %v", err)
			}
			if payload["from"] != model {
				t.Fatalf("unexpected pull model payload: %v", payload)
			}
			pullSeen = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/models/ai/smollm2":
			deleteSeen = true
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	t.Setenv(modelRunnerBasesEnv, ts.URL)
	t.Setenv(modelRunnerBaseEnv, "")

	svc := &Service{}
	ctx := context.Background()

	status := svc.ModelRunnerStatus(ctx)
	if !status.Available {
		t.Fatalf("expected model runner available, status=%+v", status)
	}
	if status.BaseURL != ts.URL {
		t.Fatalf("unexpected base URL: %q", status.BaseURL)
	}

	models, st, err := svc.ModelRunnerListModels(ctx)
	if err != nil {
		t.Fatalf("ModelRunnerListModels error: %v", err)
	}
	if !st.Available {
		t.Fatalf("expected available status, got %+v", st)
	}
	if !strings.Contains(string(models), "smollm2") {
		t.Fatalf("unexpected model list payload: %s", string(models))
	}

	if st, err := svc.ModelRunnerPullModel(ctx, model); err != nil {
		t.Fatalf("ModelRunnerPullModel error: %v (status=%+v)", err, st)
	}
	if !pullSeen {
		t.Fatalf("expected pull request to be sent")
	}

	if st, err := svc.ModelRunnerDeleteModel(ctx, model); err != nil {
		t.Fatalf("ModelRunnerDeleteModel error: %v (status=%+v)", err, st)
	}
	if !deleteSeen {
		t.Fatalf("expected delete request to be sent")
	}
}

func TestModelRunnerDeleteModelRejectsInvalidName(t *testing.T) {
	client := newModelRunnerClient()
	err := client.deleteModel(context.Background(), "http://localhost:12434", "smollm2")
	if err == nil {
		t.Fatalf("expected invalid model format error")
	}
}
