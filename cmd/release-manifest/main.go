// release-manifest signs connector artifacts; it never uploads or activates them.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	protocol "github.com/antgpu/connector-protocol"
	"github.com/antgpu/connector/internal/securefs"
)

func main() {
	keyDir := flag.String("keygen-dir", "", "create a new offline signing key in this private directory")
	version := flag.String("version", "", "release version")
	dist := flag.String("dist", "dist", "directory containing release binaries")
	base := flag.String("base-url", "https://api.antgpu.ai", "artifact origin")
	publicPath := flag.String("public-key", ".github/connector-update.pub", "reviewed public key")
	flag.Parse()
	var err error
	if *keyDir != "" {
		err = keygen(*keyDir)
	} else {
		err = sign(*version, *dist, *base, *publicPath)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func keygen(dir string) error {
	if err := securefs.PrivateDir(dir); err != nil {
		return err
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	for _, item := range []struct {
		name string
		data []byte
	}{{"private.key", private}, {"public.key", public}} {
		f, err := os.OpenFile(filepath.Join(dir, item.name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, writeErr := fmt.Fprintln(f, base64.StdEncoding.EncodeToString(item.data))
		closeErr := f.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func sign(version, dist, base, publicPath string) error {
	if err := securefs.ValidateVersion(version); err != nil {
		return err
	}
	public, err := os.ReadFile(publicPath)
	if err != nil {
		return err
	}
	manifest := protocol.UpdateManifest{Version: version}
	for _, arch := range []string{"amd64", "arm64"} {
		name := "connect-linux-" + arch
		file, err := os.Open(filepath.Join(dist, version, name))
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		manifest.Binaries = append(manifest.Binaries, protocol.UpdateArtifact{OS: "linux", Arch: arch, URL: strings.TrimRight(base, "/") + "/internal/connect/connector/bin/" + version + "/" + name, SHA256: hex.EncodeToString(hash.Sum(nil))})
	}
	if err := protocol.SignUpdateManifest(&manifest, strings.TrimSpace(os.Getenv("CONNECTOR_SIGNING_KEY"))); err != nil {
		return err
	}
	if err := protocol.VerifyUpdateManifest(manifest, strings.TrimSpace(string(public))); err != nil {
		return fmt.Errorf("signing key does not match pinned public key: %w", err)
	}
	payload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dist, version, "connector-manifest.json"), append(payload, '\n'), 0o644)
}
