package protocol

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
)

type UpdateManifest struct {
	Version   string           `json:"version"`
	Binaries  []UpdateArtifact `json:"binaries"`
	Signature string           `json:"signature,omitempty"`
}

type UpdateArtifact struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

var updateVersion = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func updatePayload(manifest UpdateManifest) ([]byte, error) {
	if !updateVersion.MatchString(manifest.Version) || len(manifest.Binaries) == 0 {
		return nil, fmt.Errorf("invalid update manifest")
	}
	seen := map[string]bool{}
	for _, artifact := range manifest.Binaries {
		key := artifact.OS + "/" + artifact.Arch
		if seen[key] || artifact.OS == "" || artifact.Arch == "" {
			return nil, fmt.Errorf("invalid or duplicate update platform")
		}
		seen[key] = true
		u, err := url.Parse(artifact.URL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			return nil, fmt.Errorf("invalid update artifact URL")
		}
		sum, err := hex.DecodeString(artifact.SHA256)
		if err != nil || len(sum) != 32 {
			return nil, fmt.Errorf("invalid artifact checksum")
		}
	}
	// Typed encoding is canonical across JSONB key reordering in the backend.
	manifest.Signature = ""
	payload, err := json.Marshal(manifest)
	return append([]byte("antgpu-connector-update-v1\n"), payload...), err
}

func SignUpdateManifest(manifest *UpdateManifest, encodedKey string) error {
	key, err := base64.StdEncoding.DecodeString(encodedKey)
	if err != nil || len(key) != ed25519.PrivateKeySize {
		return fmt.Errorf("invalid Ed25519 signing key")
	}
	payload, err := updatePayload(*manifest)
	if err != nil {
		return err
	}
	manifest.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(key, payload))
	return nil
}

func VerifyUpdateManifest(manifest UpdateManifest, encodedKey string) error {
	key, err := base64.StdEncoding.DecodeString(encodedKey)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return fmt.Errorf("trusted update public key is missing or invalid")
	}
	signature, err := base64.StdEncoding.DecodeString(manifest.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return fmt.Errorf("update signature is missing or invalid")
	}
	payload, err := updatePayload(manifest)
	if err != nil {
		return err
	}
	if !ed25519.Verify(key, payload, signature) {
		return fmt.Errorf("update signature verification failed")
	}
	return nil
}
