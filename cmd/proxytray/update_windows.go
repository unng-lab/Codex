//go:build windows

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
	"brainhub/internal/update"

	"github.com/getlantern/systray"
	"go.uber.org/zap"
)

type trayUpdateResult struct {
	ManifestVersion string
	NotesURL        string
}

func (a *trayApp) startUpdateLoop() {
	mode := normalizeTrayUpdateMode(a.cfg.AutoUpdateMode)
	if strings.TrimSpace(a.cfg.UpdateManifestURL) == "" || mode == "off" {
		a.setUpdateStatus("Updates disabled", false, nil)
		return
	}

	if mode == "apply" {
		go a.checkForUpdates(true)
	} else {
		go a.checkForUpdates(false)
	}

	if a.cfg.UpdateCheckInterval <= 0 {
		return
	}

	ticker := time.NewTicker(a.cfg.UpdateCheckInterval)
	defer ticker.Stop()
	for range ticker.C {
		if a.isStopped() {
			return
		}
		a.checkForUpdates(mode == "apply")
	}
}

func (a *trayApp) checkForUpdates(apply bool) {
	a.mu.Lock()
	if a.stopped || a.updateBusy {
		a.mu.Unlock()
		return
	}
	a.updateBusy = true
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.updateBusy = false
		a.mu.Unlock()
	}()

	a.setUpdateStatus("Checking for updates...", false, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	result, err := update.Check(ctx, update.Config{
		ManifestURL: strings.TrimSpace(a.cfg.UpdateManifestURL),
		Channel:     strings.TrimSpace(a.cfg.UpdateChannel),
		Current:     buildinfo.Version,
		PublicKey:   strings.TrimSpace(a.cfg.UpdatePublicKey),
	})
	if err != nil {
		if a.logger != nil {
			a.logger.Warn("update check failed", zap.Error(err))
		}
		a.setUpdateStatus("Update check failed", false, nil)
		return
	}
	if !result.Available {
		a.setUpdateStatus("No updates available", false, nil)
		return
	}

	updateResult := &trayUpdateResult{
		ManifestVersion: result.Manifest.Version,
		NotesURL:        result.Manifest.NotesURL,
	}
	a.setUpdateStatus("Update available: "+result.Manifest.Version, true, updateResult)
	if apply {
		a.installUpdate(result)
	}
}

func (a *trayApp) installAvailableUpdate() {
	a.mu.Lock()
	result := a.updateResult
	a.mu.Unlock()
	if result == nil {
		a.setStatus("No staged update information yet")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	check, err := update.Check(ctx, update.Config{
		ManifestURL: strings.TrimSpace(a.cfg.UpdateManifestURL),
		Channel:     strings.TrimSpace(a.cfg.UpdateChannel),
		Current:     buildinfo.Version,
		PublicKey:   strings.TrimSpace(a.cfg.UpdatePublicKey),
	})
	if err != nil {
		if a.logger != nil {
			a.logger.Warn("refresh update check failed", zap.Error(err))
		}
		a.setStatus("Update refresh failed")
		return
	}
	if !check.Available || check.Manifest.Version != result.ManifestVersion {
		a.setUpdateStatus("No updates available", false, nil)
		return
	}
	a.installUpdate(check)
}

func (a *trayApp) installUpdate(result update.CheckResult) {
	lock, err := update.AcquireLock(authstore.DefaultUpdateLockPath())
	if err != nil {
		if errors.Is(err, update.ErrUpdateLocked) {
			a.setStatus("Another update is already running")
			return
		}
		if a.logger != nil {
			a.logger.Error("acquire update lock", zap.Error(err))
		}
		a.setStatus("Update lock failed")
		return
	}
	defer func() {
		if lock != nil {
			_ = lock.Release()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	a.setUpdateStatus("Downloading update "+result.Manifest.Version+"...", false, a.updateResult)
	staged, err := update.DownloadAndStage(ctx, nil, result, authstore.DefaultDownloadsDir(), authstore.DefaultUpdatesDir())
	if err != nil {
		if a.logger != nil {
			a.logger.Error("download update", zap.Error(err))
		}
		a.setStatus("Update download failed")
		return
	}

	execPath, err := os.Executable()
	if err != nil {
		if a.logger != nil {
			a.logger.Error("resolve executable", zap.Error(err))
		}
		a.setStatus("Update install failed")
		return
	}
	updaterPath := filepath.Join(staged.StagingDir, trayUpdaterBinaryName())
	planPath := filepath.Join(authstore.DefaultDownloadsDir(), fmt.Sprintf("apply-tray-%s.json", staged.Version))
	plan := update.ApplyPlan{
		Mode:              "tray",
		WaitPID:           os.Getpid(),
		TargetDir:         filepath.Dir(execPath),
		StagingDir:        staged.StagingDir,
		CurrentVersion:    buildinfo.Version,
		TargetVersion:     staged.Version,
		LockFile:          lock.Path(),
		RestartExecutable: execPath,
		RestartArgs:       append([]string(nil), os.Args[1:]...),
	}
	if err := update.WritePlan(planPath, plan); err != nil {
		if a.logger != nil {
			a.logger.Error("write update plan", zap.Error(err))
		}
		a.setStatus("Update install failed")
		return
	}

	cmd := exec.Command(updaterPath, "--plan-file", planPath)
	if err := cmd.Start(); err != nil {
		if a.logger != nil {
			a.logger.Error("start updater", zap.Error(err))
		}
		a.setStatus("Update install failed")
		return
	}
	lock = nil

	a.setStatus("Installing update " + staged.Version)
	systrayQuit()
}

func (a *trayApp) setUpdateStatus(text string, available bool, result *trayUpdateResult) {
	a.mu.Lock()
	a.updateAvailable = available
	a.updateResult = result
	a.mu.Unlock()

	if a.checkUpdatesItem != nil {
		a.checkUpdatesItem.SetTitle(text)
	}
	if a.installUpdateItem != nil {
		if available {
			a.installUpdateItem.Enable()
			a.installUpdateItem.SetTitle("Install update")
		} else {
			a.installUpdateItem.Disable()
			a.installUpdateItem.SetTitle("Install update")
		}
	}
}

func (a *trayApp) isStopped() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stopped
}

func systrayQuit() {
	systray.Quit()
}

func normalizeTrayUpdateMode(mode string) string {
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

func trayUpdaterBinaryName() string {
	if strings.EqualFold(filepath.Ext(os.Args[0]), ".exe") {
		return "updater.exe"
	}
	return "updater"
}
