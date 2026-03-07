package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"brainhub/internal/authstore"
	"brainhub/internal/buildinfo"
	"brainhub/internal/proxyapp"
	"brainhub/internal/update"

	"go.uber.org/zap"
)

type proxyUpdateConfig struct {
	ManifestURL        string
	Channel            string
	PublicKey          string
	Mode               string
	CheckInterval      time.Duration
	WindowsServiceName string
}

func startProxyAutoUpdateLoop(app *proxyapp.App, logger *zap.Logger, cfg proxyUpdateConfig) {
	mode := normalizeUpdateMode(cfg.Mode)
	manifestURL := strings.TrimSpace(cfg.ManifestURL)
	if manifestURL == "" || mode == "off" {
		return
	}

	run := func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		applied, err := checkAndMaybeApplyProxyUpdate(ctx, app, logger, cfg, mode)
		if err != nil {
			logger.Warn("update check failed", zap.Error(err))
		}
		return applied
	}

	go func() {
		if run() {
			return
		}
		if cfg.CheckInterval <= 0 {
			return
		}

		ticker := time.NewTicker(cfg.CheckInterval)
		defer ticker.Stop()
		for range ticker.C {
			if run() {
				return
			}
		}
	}()
}

func checkAndMaybeApplyProxyUpdate(ctx context.Context, app *proxyapp.App, logger *zap.Logger, cfg proxyUpdateConfig, mode string) (bool, error) {
	result, err := update.Check(ctx, update.Config{
		ManifestURL: strings.TrimSpace(cfg.ManifestURL),
		Channel:     strings.TrimSpace(cfg.Channel),
		Current:     buildinfo.Version,
		PublicKey:   strings.TrimSpace(cfg.PublicKey),
	})
	if err != nil {
		return false, err
	}
	if !result.Available {
		logger.Info("no updates available", zap.String("current", result.Current), zap.String("platform", result.Platform))
		return false, nil
	}

	logger.Info(
		"update available",
		zap.String("current", result.Current),
		zap.String("latest", result.Manifest.Version),
		zap.String("platform", result.Platform),
		zap.String("notes_url", result.Manifest.NotesURL),
	)
	if mode != "apply" {
		return false, nil
	}

	lock, err := update.AcquireLock(authstore.DefaultUpdateLockPath())
	if err != nil {
		if errors.Is(err, update.ErrUpdateLocked) {
			logger.Info("update already in progress", zap.String("lock_file", authstore.DefaultUpdateLockPath()))
			return false, nil
		}
		return false, err
	}
	defer func() {
		if lock != nil {
			_ = lock.Release()
		}
	}()

	staged, err := update.DownloadAndStage(ctx, nil, result, authstore.DefaultDownloadsDir(), authstore.DefaultUpdatesDir())
	if err != nil {
		return false, err
	}

	execPath, err := os.Executable()
	if err != nil {
		return false, fmt.Errorf("locate current executable: %w", err)
	}
	targetDir := filepath.Dir(execPath)
	updaterPath := filepath.Join(staged.StagingDir, updaterBinaryName())
	planPath := filepath.Join(authstore.DefaultDownloadsDir(), fmt.Sprintf("apply-%s.json", staged.Version))

	plan := update.ApplyPlan{
		Mode:              proxyUpdateMode(cfg.WindowsServiceName),
		WaitPID:           os.Getpid(),
		TargetDir:         targetDir,
		StagingDir:        staged.StagingDir,
		CurrentVersion:    buildinfo.Version,
		TargetVersion:     staged.Version,
		ServiceName:       strings.TrimSpace(cfg.WindowsServiceName),
		LockFile:          lock.Path(),
		RestartExecutable: execPath,
		RestartArgs:       append([]string(nil), os.Args[1:]...),
	}
	if plan.Mode == "service" {
		plan.RestartExecutable = ""
		plan.RestartArgs = nil
	}

	if err := update.WritePlan(planPath, plan); err != nil {
		return false, err
	}

	cmd := exec.Command(updaterPath, "--plan-file", planPath)
	if err := cmd.Start(); err != nil {
		return false, fmt.Errorf("start updater: %w", err)
	}
	lock = nil

	logger.Info("update staged; shutting down current process", zap.String("version", staged.Version))
	go func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = app.Stop(stopCtx)
	}()
	return true, nil
}

func normalizeUpdateMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "off":
		return "off"
	case "check":
		return "check"
	case "apply":
		return "apply"
	default:
		return "off"
	}
}

func proxyUpdateMode(serviceName string) string {
	if strings.TrimSpace(serviceName) != "" {
		return "service"
	}
	return "console"
}

func updaterBinaryName() string {
	if strings.EqualFold(filepath.Ext(os.Args[0]), ".exe") {
		return "updater.exe"
	}
	return "updater"
}
