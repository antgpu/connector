package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	protocol "github.com/antgpu/connector-protocol"
	"os"
	"strings"
	"testing"
)

func TestVerifySignedWindowsArtifact(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifest := protocol.UpdateManifest{Version: "windows-test", Binaries: []protocol.UpdateArtifact{{OS: "windows", Arch: "amd64", URL: "https://example.com/connect?name=<a>&x=2", SHA256: strings.Repeat("a", 64)}}}
	if err := protocol.SignUpdateManifest(&manifest, base64.StdEncoding.EncodeToString(key)); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(manifest)
	encoded := base64.StdEncoding.EncodeToString(pub)
	selected, err := verify(payload, "https://example.com/manifest", encoded)
	if err != nil || selected["version"] != "windows-test" {
		t.Fatalf("valid signature: %v %v", selected, err)
	}
	if _, err := verify(payload, "https://other.example/manifest", encoded); err == nil {
		t.Fatal("accepted cross-origin artifact")
	}
	manifest.Version = "tampered"
	payload, _ = json.Marshal(manifest)
	if _, err := verify(payload, "https://example.com/manifest", encoded); err == nil {
		t.Fatal("accepted tampered signature")
	}
	manifest.Signature = ""
	payload, _ = json.Marshal(manifest)
	if _, err := verify(payload, "https://example.com/manifest", encoded); err == nil {
		t.Fatal("accepted unsigned manifest")
	}
}

func TestPinnedKeyMatchesConnector(t *testing.T) {
	data, err := os.ReadFile("../../.github/connector-update.pub")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != publicKey {
		t.Fatal("installer verifier and updater pins differ")
	}
}
