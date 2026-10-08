package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	internaldocker "github.com/antgpu/connector/internal/docker"
)

func TestEffectiveRuntimeContext(t *testing.T) {
	for _, tc := range []struct {
		body string
		want int
	}{
		{`{"default_generation_settings":{"n_ctx":128},"n_ctx_train":512}`, 128},
		{`{"n_ctx_train":512}`, 0},
		{`{"default_generation_settings":{"n_ctx":-1}}`, 0},
		{`not json`, 0},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/props" || r.Header.Get("Authorization") != "Bearer secret" {
				t.Error("unexpected metadata request")
			}
			fmt.Fprint(w, tc.body)
		}))
		u, _ := url.Parse(srv.URL)
		got := runtimeContextTokens(context.Background(), internaldocker.RuntimeTarget{Kind: "openai", URL: u, Token: "secret"})
		srv.Close()
		if got != tc.want {
			t.Fatalf("got %d want %d", got, tc.want)
		}
	}
}
