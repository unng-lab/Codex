package update

import (
	"archive/zip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const DefaultHTTPTimeout = 30 * time.Second

type Config struct {
	ManifestURL string
	Channel     string
	Current     string
	Platform    string
	PublicKey   string
	Client      *http.Client
}

type Manifest struct {
	Version     string                    `json:"version"`
	Channel     string                    `json:"channel"`
	PublishedAt string                    `json:"published_at"`
	NotesURL    string                    `json:"notes_url"`
	Platforms   map[string]BundleArtifact `json:"platforms"`
	Signature   string                    `json:"signature"`
}

type BundleArtifact struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

type CheckResult struct {
	Available bool
	Current   string
	Platform  string
	Manifest  Manifest
	Artifact  BundleArtifact
}

type StagedRelease struct {
	Version      string
	Platform     string
	DownloadPath string
	StagingDir   string
}

type ApplyPlan struct {
	Mode              string   `json:"mode"`
	WaitPID           int      `json:"wait_pid"`
	TargetDir         string   `json:"target_dir"`
	StagingDir        string   `json:"staging_dir"`
	CurrentVersion    string   `json:"current_version"`
	TargetVersion     string   `json:"target_version"`
	ServiceName       string   `json:"service_name,omitempty"`
	LockFile          string   `json:"lock_file,omitempty"`
	RestartExecutable string   `json:"restart_executable,omitempty"`
	RestartArgs       []string `json:"restart_args,omitempty"`
}

type Lock struct {
	path string
}

var ErrUpdateLocked = errors.New("update already in progress")

func Check(ctx context.Context, cfg Config) (CheckResult, error) {
	manifestURL := strings.TrimSpace(cfg.ManifestURL)
	if manifestURL == "" {
		return CheckResult{}, fmt.Errorf("update manifest URL is required")
	}

	manifest, err := FetchManifest(ctx, manifestURL, cfg.Client)
	if err != nil {
		return CheckResult{}, err
	}
	if err := VerifyManifestSignature(manifest, cfg.PublicKey); err != nil {
		return CheckResult{}, err
	}

	platform := strings.TrimSpace(cfg.Platform)
	if platform == "" {
		platform = CurrentPlatform()
	}

	artifact, ok := manifest.Platforms[platform]
	if !ok {
		return CheckResult{}, fmt.Errorf("manifest does not contain platform %q", platform)
	}
	if strings.TrimSpace(artifact.URL) == "" {
		return CheckResult{}, fmt.Errorf("manifest platform %q is missing artifact URL", platform)
	}
	if strings.TrimSpace(artifact.SHA256) == "" {
		return CheckResult{}, fmt.Errorf("manifest platform %q is missing artifact sha256", platform)
	}

	current := strings.TrimSpace(cfg.Current)
	if current == "" {
		current = "dev"
	}

	channel := strings.TrimSpace(cfg.Channel)
	if channel != "" && strings.TrimSpace(manifest.Channel) != "" && manifest.Channel != channel {
		return CheckResult{}, fmt.Errorf("manifest channel mismatch: got %q want %q", manifest.Channel, channel)
	}

	return CheckResult{
		Available: CompareVersions(current, manifest.Version) < 0,
		Current:   current,
		Platform:  platform,
		Manifest:  manifest,
		Artifact:  artifact,
	}, nil
}

func FetchManifest(ctx context.Context, url string, client *http.Client) (Manifest, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Manifest{}, fmt.Errorf("build manifest request: %w", err)
	}

	resp, err := httpClient(client).Do(req)
	if err != nil {
		return Manifest{}, fmt.Errorf("fetch manifest: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Manifest{}, fmt.Errorf("fetch manifest: unexpected status %s", resp.Status)
	}

	var manifest Manifest
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode manifest: %w", err)
	}

	if strings.TrimSpace(manifest.Version) == "" {
		return Manifest{}, fmt.Errorf("manifest is missing version")
	}
	if len(manifest.Platforms) == 0 {
		return Manifest{}, fmt.Errorf("manifest is missing platforms")
	}
	return manifest, nil
}

func VerifyManifestSignature(manifest Manifest, publicKey string) error {
	signature := strings.TrimSpace(manifest.Signature)
	publicKey = strings.TrimSpace(publicKey)
	switch {
	case signature == "" && publicKey == "":
		return nil
	case signature == "" && publicKey != "":
		return fmt.Errorf("manifest signature is missing")
	case signature != "" && publicKey == "":
		return fmt.Errorf("manifest signature is present but no public key is configured")
	}

	pub, err := base64.StdEncoding.DecodeString(publicKey)
	if err != nil {
		return fmt.Errorf("decode update public key: %w", err)
	}
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("update public key must be %d bytes", ed25519.PublicKeySize)
	}

	sig, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return fmt.Errorf("decode manifest signature: %w", err)
	}
	if len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("manifest signature must be %d bytes", ed25519.SignatureSize)
	}

	payload, err := manifestPayloadBytes(manifest)
	if err != nil {
		return err
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), payload, sig) {
		return fmt.Errorf("manifest signature verification failed")
	}
	return nil
}

func AcquireLock(path string) (*Lock, error) {
	lockPath := strings.TrimSpace(path)
	if lockPath == "" {
		return nil, fmt.Errorf("lock path is required")
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}

	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, ErrUpdateLocked
		}
		return nil, fmt.Errorf("create update lock: %w", err)
	}
	content := fmt.Sprintf("pid=%d\ncreated_at=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))
	if _, err := io.WriteString(f, content); err != nil {
		_ = f.Close()
		_ = os.Remove(lockPath)
		return nil, fmt.Errorf("write update lock: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(lockPath)
		return nil, fmt.Errorf("close update lock: %w", err)
	}
	return &Lock{path: lockPath}, nil
}

func (l *Lock) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

func (l *Lock) Release() error {
	if l == nil || strings.TrimSpace(l.path) == "" {
		return nil
	}
	if err := os.Remove(l.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove update lock: %w", err)
	}
	return nil
}

func DownloadAndStage(ctx context.Context, client *http.Client, result CheckResult, downloadsDir string, stagingRoot string) (StagedRelease, error) {
	if !result.Available {
		return StagedRelease{}, fmt.Errorf("no update available")
	}

	if err := os.MkdirAll(downloadsDir, 0o700); err != nil {
		return StagedRelease{}, fmt.Errorf("create downloads dir: %w", err)
	}

	stageDir := filepath.Join(stagingRoot, result.Manifest.Version, result.Platform)
	if err := os.RemoveAll(stageDir); err != nil {
		return StagedRelease{}, fmt.Errorf("cleanup staging dir: %w", err)
	}
	if err := os.MkdirAll(stageDir, 0o700); err != nil {
		return StagedRelease{}, fmt.Errorf("create staging dir: %w", err)
	}

	downloadPath := filepath.Join(downloadsDir, fmt.Sprintf("%s-%s.zip", result.Manifest.Version, result.Platform))
	if err := downloadFile(ctx, httpClient(client), result.Artifact.URL, downloadPath); err != nil {
		return StagedRelease{}, err
	}
	if err := verifyFileSHA256(downloadPath, result.Artifact.SHA256); err != nil {
		return StagedRelease{}, err
	}
	if err := unzip(downloadPath, stageDir); err != nil {
		return StagedRelease{}, err
	}

	return StagedRelease{
		Version:      result.Manifest.Version,
		Platform:     result.Platform,
		DownloadPath: downloadPath,
		StagingDir:   stageDir,
	}, nil
}

func CurrentPlatform() string {
	return runtime.GOOS + "-" + runtime.GOARCH
}

func CompareVersions(current string, latest string) int {
	a := parseVersion(current)
	b := parseVersion(latest)

	for i := 0; i < 3; i++ {
		switch {
		case a[i] < b[i]:
			return -1
		case a[i] > b[i]:
			return 1
		}
	}
	return 0
}

func WritePlan(path string, plan ApplyPlan) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("plan path is required")
	}
	b, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal apply plan: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create plan dir: %w", err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("write apply plan: %w", err)
	}
	return nil
}

func ReadPlan(path string) (ApplyPlan, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return ApplyPlan{}, fmt.Errorf("read apply plan: %w", err)
	}

	var plan ApplyPlan
	if err := json.Unmarshal(b, &plan); err != nil {
		return ApplyPlan{}, fmt.Errorf("parse apply plan: %w", err)
	}
	if strings.TrimSpace(plan.TargetDir) == "" {
		return ApplyPlan{}, fmt.Errorf("apply plan is missing target_dir")
	}
	if strings.TrimSpace(plan.StagingDir) == "" {
		return ApplyPlan{}, fmt.Errorf("apply plan is missing staging_dir")
	}
	return plan, nil
}

func httpClient(client *http.Client) *http.Client {
	if client != nil {
		return client
	}
	return &http.Client{Timeout: DefaultHTTPTimeout}
}

func manifestPayloadBytes(manifest Manifest) ([]byte, error) {
	payloadManifest := manifest
	payloadManifest.Signature = ""
	b, err := json.Marshal(payloadManifest)
	if err != nil {
		return nil, fmt.Errorf("marshal manifest payload: %w", err)
	}
	return b, nil
}

func downloadFile(ctx context.Context, client *http.Client, url string, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build download request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download release: unexpected status %s", resp.Status)
	}

	tmpPath := dst + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("create temp download: %w", err)
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write temp download: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp download: %w", err)
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("move temp download: %w", err)
	}
	return nil
}

func verifyFileSHA256(path string, expected string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open download for sha256: %w", err)
	}
	defer f.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return fmt.Errorf("hash download: %w", err)
	}

	sum := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(sum, strings.TrimSpace(expected)) {
		return fmt.Errorf("sha256 mismatch: got %s want %s", sum, expected)
	}
	return nil
}

func unzip(src string, dst string) error {
	reader, err := zip.OpenReader(src)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer reader.Close()

	for _, file := range reader.File {
		targetPath := filepath.Join(dst, file.Name)
		if !strings.HasPrefix(targetPath, filepath.Clean(dst)+string(os.PathSeparator)) && filepath.Clean(targetPath) != filepath.Clean(dst) {
			return fmt.Errorf("zip entry escapes staging dir: %s", file.Name)
		}
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(targetPath, 0o700); err != nil {
				return fmt.Errorf("create dir from zip: %w", err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o700); err != nil {
			return fmt.Errorf("create parent dir from zip: %w", err)
		}

		rc, err := file.Open()
		if err != nil {
			return fmt.Errorf("open zip entry: %w", err)
		}
		mode := file.Mode()
		if mode == 0 {
			mode = 0o755
		}
		out, err := os.OpenFile(targetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
		if err != nil {
			rc.Close()
			return fmt.Errorf("create staged file: %w", err)
		}
		if _, err := io.Copy(out, rc); err != nil {
			out.Close()
			rc.Close()
			return fmt.Errorf("write staged file: %w", err)
		}
		if err := out.Close(); err != nil {
			rc.Close()
			return fmt.Errorf("close staged file: %w", err)
		}
		if err := rc.Close(); err != nil {
			return fmt.Errorf("close zip entry: %w", err)
		}
	}
	return nil
}

func parseVersion(v string) [3]int {
	var out [3]int
	v = strings.TrimSpace(strings.TrimPrefix(v, "v"))
	if v == "" {
		return out
	}

	main := strings.SplitN(v, "-", 2)[0]
	parts := strings.Split(main, ".")
	for i := 0; i < len(parts) && i < len(out); i++ {
		part := strings.TrimSpace(parts[i])
		for _, ch := range part {
			if ch < '0' || ch > '9' {
				part = strings.SplitN(part, string(ch), 2)[0]
				break
			}
		}
		var value int
		for _, ch := range part {
			if ch < '0' || ch > '9' {
				break
			}
			value = value*10 + int(ch-'0')
		}
		out[i] = value
	}
	return out
}
