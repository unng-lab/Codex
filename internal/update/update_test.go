package update

import (
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		current string
		latest  string
		want    int
	}{
		{name: "same", current: "v1.2.3", latest: "v1.2.3", want: 0},
		{name: "older", current: "v1.2.3", latest: "v1.3.0", want: -1},
		{name: "newer", current: "v2.0.0", latest: "v1.9.9", want: 1},
		{name: "dev treated as zero", current: "dev", latest: "v0.0.1", want: -1},
		{name: "no v prefix", current: "1.10.0", latest: "1.9.9", want: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := CompareVersions(tt.current, tt.latest); got != tt.want {
				t.Fatalf("CompareVersions(%q, %q) = %d, want %d", tt.current, tt.latest, got, tt.want)
			}
		})
	}
}

func TestVerifyManifestSignature(t *testing.T) {
	t.Parallel()

	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	manifest := Manifest{
		Version:   "v1.2.3",
		Channel:   "stable",
		Platforms: map[string]BundleArtifact{"windows-amd64": {URL: "https://example.test/bundle.zip", SHA256: "abc"}},
	}
	payload, err := manifestPayloadBytes(manifest)
	if err != nil {
		t.Fatalf("manifest payload: %v", err)
	}
	manifest.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))

	if err := VerifyManifestSignature(manifest, base64.StdEncoding.EncodeToString(publicKey)); err != nil {
		t.Fatalf("VerifyManifestSignature() error = %v", err)
	}

	manifest.Version = "v1.2.4"
	if err := VerifyManifestSignature(manifest, base64.StdEncoding.EncodeToString(publicKey)); err == nil {
		t.Fatalf("VerifyManifestSignature() expected failure for tampered manifest")
	}
}

func TestAcquireLock(t *testing.T) {
	t.Parallel()

	lockPath := filepath.Join(t.TempDir(), "update.lock")
	lock, err := AcquireLock(lockPath)
	if err != nil {
		t.Fatalf("AcquireLock() error = %v", err)
	}
	defer func() {
		_ = lock.Release()
	}()

	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("stat lock file: %v", err)
	}

	if _, err := AcquireLock(lockPath); err != ErrUpdateLocked {
		t.Fatalf("AcquireLock() second call error = %v, want %v", err, ErrUpdateLocked)
	}

	if err := lock.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("lock file still exists: %v", err)
	}
}
