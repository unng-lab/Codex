package authstore

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	DirName            = ".brainhub"
	FileName           = "auth.json"
	UpdatesDirName     = "updates"
	DownloadsDirName   = "downloads"
	UpdateLockFileName = "update.lock"
)

func DefaultAuthHomeDir() string {
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, DirName)
	}
	if wd, err := os.Getwd(); err == nil && strings.TrimSpace(wd) != "" {
		return filepath.Join(wd, DirName)
	}
	return DirName
}

func DefaultAuthFilePath() string {
	return filepath.Join(DefaultAuthHomeDir(), FileName)
}

func AuthFilePath(home string) string {
	p := strings.TrimSpace(home)
	if p == "" {
		return DefaultAuthFilePath()
	}
	return filepath.Join(p, FileName)
}

func DefaultUpdatesDir() string {
	return filepath.Join(DefaultAuthHomeDir(), UpdatesDirName)
}

func DefaultDownloadsDir() string {
	return filepath.Join(DefaultAuthHomeDir(), DownloadsDirName)
}

func DefaultUpdateLockPath() string {
	return filepath.Join(DefaultAuthHomeDir(), UpdateLockFileName)
}
