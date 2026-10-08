package protocol

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestUpdateManifestSignatureBindsEveryField(t *testing.T) {
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	public := base64.StdEncoding.EncodeToString(pub)
	manifest := UpdateManifest{Version: "2026.10.08", Binaries: []UpdateArtifact{{OS: "linux", Arch: "amd64", URL: "https://api.example/artifact", SHA256: strings.Repeat("a", 64)}}}
	if err := SignUpdateManifest(&manifest, base64.StdEncoding.EncodeToString(private)); err != nil {
		t.Fatal(err)
	}
	if err := VerifyUpdateManifest(manifest, public); err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*UpdateManifest){
		"version":  func(m *UpdateManifest) { m.Version = "2026.10.09" },
		"checksum": func(m *UpdateManifest) { m.Binaries[0].SHA256 = strings.Repeat("b", 64) },
		"url":      func(m *UpdateManifest) { m.Binaries[0].URL = "https://evil.example/artifact" },
		"platform": func(m *UpdateManifest) { m.Binaries[0].Arch = "arm64" },
		"unsigned": func(m *UpdateManifest) { m.Signature = "" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			m := manifest
			m.Binaries = append([]UpdateArtifact(nil), manifest.Binaries...)
			mutate(&m)
			if VerifyUpdateManifest(m, public) == nil {
				t.Fatal("accepted changed signed payload")
			}
		})
	}
	if VerifyUpdateManifest(manifest, "") == nil {
		t.Fatal("accepted missing trust key")
	}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	if VerifyUpdateManifest(manifest, base64.StdEncoding.EncodeToString(other)) == nil {
		t.Fatal("accepted foreign signing key")
	}
	// JSONB may reorder object keys; typed canonicalization must survive it.
	data, _ := json.Marshal(manifest)
	var fields map[string]any
	_ = json.Unmarshal(data, &fields)
	reordered, _ := json.Marshal(fields)
	var roundtrip UpdateManifest
	if err := json.Unmarshal(reordered, &roundtrip); err != nil {
		t.Fatal(err)
	}
	if err := VerifyUpdateManifest(roundtrip, public); err != nil {
		t.Fatal(err)
	}
}

func TestSignManifestRejectsUnsafeMetadata(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	encoded := base64.StdEncoding.EncodeToString(key)
	for _, version := range []string{"../root", "..", "/tmp"} {
		m := UpdateManifest{Version: version, Binaries: []UpdateArtifact{{OS: "linux", Arch: "amd64", URL: "https://api.example/a", SHA256: strings.Repeat("a", 64)}}}
		if SignUpdateManifest(&m, encoded) == nil {
			t.Errorf("signed unsafe version %q", version)
		}
	}
}
