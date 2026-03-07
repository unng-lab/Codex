package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"brainhub/internal/update"
)

func main() {
	fs := flag.NewFlagSet("updater", flag.ExitOnError)
	planFile := fs.String("plan-file", "", "Path to updater plan JSON file")
	if err := fs.Parse(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(2)
	}

	plan, err := update.ReadPlan(*planFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}

	if err := run(plan); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
}

func run(plan update.ApplyPlan) error {
	defer func() {
		if strings.TrimSpace(plan.LockFile) != "" {
			_ = os.Remove(plan.LockFile)
		}
	}()

	if plan.Mode == "service" && strings.TrimSpace(plan.ServiceName) != "" {
		if err := stopService(plan.ServiceName); err != nil {
			return err
		}
	}

	if err := waitForExit(plan.WaitPID, 30*time.Second); err != nil {
		return err
	}

	backups, err := applyStagedFiles(plan.StagingDir, plan.TargetDir)
	if err != nil {
		return err
	}

	restartErr := restart(plan)
	if restartErr == nil {
		cleanupBackups(backups)
		return nil
	}

	if rollbackErr := rollback(backups); rollbackErr != nil {
		return fmt.Errorf("restart failed: %v; rollback failed: %w", restartErr, rollbackErr)
	}
	if plan.Mode == "service" && strings.TrimSpace(plan.ServiceName) != "" {
		_ = startService(plan.ServiceName)
	}
	return fmt.Errorf("restart failed after apply: %w", restartErr)
}

type backupEntry struct {
	target string
	backup string
	exists bool
}

func applyStagedFiles(stagingDir string, targetDir string) ([]backupEntry, error) {
	var backups []backupEntry

	err := filepath.WalkDir(stagingDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(stagingDir, path)
		if err != nil {
			return fmt.Errorf("compute relative staged path: %w", err)
		}
		targetPath := filepath.Join(targetDir, rel)
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o700); err != nil {
			return fmt.Errorf("create target parent: %w", err)
		}

		entry := backupEntry{target: targetPath}
		if _, err := os.Stat(targetPath); err == nil {
			entry.exists = true
			entry.backup = targetPath + ".bak"
			_ = os.Remove(entry.backup)
			if err := os.Rename(targetPath, entry.backup); err != nil {
				return fmt.Errorf("backup current file %s: %w", targetPath, err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stat target file %s: %w", targetPath, err)
		}

		if err := copyFile(path, targetPath); err != nil {
			if entry.exists && entry.backup != "" {
				_ = os.Rename(entry.backup, targetPath)
			}
			return err
		}

		backups = append(backups, entry)
		return nil
	})
	if err != nil {
		_ = rollback(backups)
		return nil, err
	}
	return backups, nil
}

func rollback(backups []backupEntry) error {
	var errs []string
	for i := len(backups) - 1; i >= 0; i-- {
		entry := backups[i]
		if entry.exists {
			if err := os.Remove(entry.target); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, err.Error())
				continue
			}
			if err := os.Rename(entry.backup, entry.target); err != nil {
				errs = append(errs, err.Error())
			}
			continue
		}
		if err := os.Remove(entry.target); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func cleanupBackups(backups []backupEntry) {
	for _, entry := range backups {
		if entry.backup == "" {
			continue
		}
		_ = os.Remove(entry.backup)
	}
}

func restart(plan update.ApplyPlan) error {
	switch plan.Mode {
	case "tray", "console":
		if strings.TrimSpace(plan.RestartExecutable) == "" {
			return nil
		}
		cmd := exec.Command(plan.RestartExecutable, plan.RestartArgs...)
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("restart executable: %w", err)
		}
		return nil
	case "service":
		if strings.TrimSpace(plan.ServiceName) == "" {
			return nil
		}
		return startService(plan.ServiceName)
	default:
		return fmt.Errorf("unsupported updater mode %q", plan.Mode)
	}
}

func stopService(name string) error {
	cmd := exec.Command("sc", "stop", name)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("stop service %q: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func startService(name string) error {
	cmd := exec.Command("sc", "start", name)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("start service %q: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func copyFile(src string, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open staged file: %w", err)
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return fmt.Errorf("stat staged file: %w", err)
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode())
	if err != nil {
		return fmt.Errorf("open target file: %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("copy staged file: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close target file: %w", err)
	}
	return nil
}

func waitForExit(pid int, timeout time.Duration) error {
	if pid <= 0 {
		return nil
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		running, err := processRunning(pid)
		if err != nil {
			return err
		}
		if !running {
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for pid %d to exit", pid)
}

func processRunning(pid int) (bool, error) {
	if runtime.GOOS == "windows" {
		cmd := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/NH")
		out, err := cmd.Output()
		if err != nil {
			return false, fmt.Errorf("tasklist pid %d: %w", pid, err)
		}
		s := strings.TrimSpace(string(out))
		if s == "" || strings.Contains(s, "No tasks are running") {
			return false, nil
		}
		return strings.Contains(s, fmt.Sprintf("%d", pid)), nil
	}

	cmd := exec.Command("ps", "-p", fmt.Sprintf("%d", pid), "-o", "pid=")
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return false, nil
		}
		return false, fmt.Errorf("ps pid %d: %w", pid, err)
	}
	return strings.TrimSpace(string(out)) != "", nil
}
