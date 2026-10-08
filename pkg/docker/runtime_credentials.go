package docker

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
)

// provisionRuntimeCredentials is opt-in: templates select a supported bootstrap
// contract, but never supply the generated secret. Docker persists the secret
// with this container; a new container receives a different identity.
func provisionRuntimeCredentials(labels map[string]string, env []string) ([]string, error) {
	if labels["ai.antgpu.runtime.bootstrap"] == "" {
		return env, nil
	}
	if labels["ai.antgpu.runtime.bootstrap"] != "unsloth-studio" || labels["ai.antgpu.runtime"] != "unsloth" {
		return nil, fmt.Errorf("unsupported runtime credential bootstrap")
	}
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "ANTGPU_UNSLOTH_") || key == "ANTGPU_RUNTIME_TOKEN" || key == "UNSLOTH_STUDIO_PASSWORD" {
			return nil, fmt.Errorf("runtime bootstrap cannot be combined with template credentials")
		}
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return nil, fmt.Errorf("generate runtime credential: %w", err)
	}
	password := base64.RawURLEncoding.EncodeToString(secret[:])
	result := append([]string(nil), env...)
	return append(result, "ANTGPU_UNSLOTH_USERNAME=unsloth", "ANTGPU_UNSLOTH_PASSWORD="+password, "UNSLOTH_STUDIO_PASSWORD="+password), nil
}
