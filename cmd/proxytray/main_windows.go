//go:build windows

package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"brainhub/internal/authstore"
	"brainhub/internal/buildinfo"
	"brainhub/internal/oauth"
	"brainhub/internal/proxyapp"
	"brainhub/internal/trayicon"

	"github.com/getlantern/systray"
	"go.uber.org/zap"
)

func main() {
	cfg, err := parseConfig(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(2)
	}

	app := &trayApp{cfg: cfg}
	systray.Run(app.onReady, app.onExit)
}

type trayConfig struct {
	Listen              string
	AuthFile            string
	Upstream            string
	ReadTimeout         time.Duration
	WriteTimeout        time.Duration
	ServiceName         string
	LogFile             string
	UpdateManifestURL   string
	UpdateChannel       string
	UpdatePublicKey     string
	AutoUpdateMode      string
	UpdateCheckInterval time.Duration
}

type trayApp struct {
	cfg trayConfig

	logger *zap.Logger
	proxy  *proxyapp.App

	mu      sync.Mutex
	login   *loginSession
	stopped bool

	statusItem        *systray.MenuItem
	loginItem         *systray.MenuItem
	copyLoginItem     *systray.MenuItem
	authItem          *systray.MenuItem
	logItem           *systray.MenuItem
	checkUpdatesItem  *systray.MenuItem
	installUpdateItem *systray.MenuItem
	quitItem          *systray.MenuItem

	stopOnce sync.Once

	updateAvailable bool
	updateResult    *trayUpdateResult
	updateBusy      bool
}

type loginSession struct {
	cancel   context.CancelFunc
	done     chan struct{}
	urlReady chan struct{}
	url      string
}

func parseConfig(args []string) (trayConfig, error) {
	fs := flag.NewFlagSet("proxytray", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	cfg := trayConfig{}
	fs.StringVar(&cfg.Listen, "listen", "127.0.0.1:8080", "HTTP listen address")
	fs.StringVar(&cfg.AuthFile, "auth-file", authstore.DefaultAuthFilePath(), "Path to auth.json")
	fs.StringVar(&cfg.Upstream, "upstream", "https://chatgpt.com", "Codex upstream base URL")
	fs.DurationVar(&cfg.ReadTimeout, "read-timeout", 30*time.Second, "HTTP server read timeout")
	fs.DurationVar(&cfg.WriteTimeout, "write-timeout", 0, "HTTP server write timeout (0 disables; recommended for streaming)")
	fs.StringVar(&cfg.ServiceName, "service-name", "brainhub-tray", "Service name for structured logs")
	fs.StringVar(&cfg.LogFile, "log-file", filepath.Join(authstore.DefaultAuthHomeDir(), "tray.log"), "Path to tray log file")
	fs.StringVar(&cfg.UpdateManifestURL, "update-manifest-url", "", "Release manifest URL for update checks")
	fs.StringVar(&cfg.UpdateChannel, "update-channel", buildinfo.Channel, "Release channel for update checks")
	fs.StringVar(&cfg.UpdatePublicKey, "update-public-key", buildinfo.UpdatePublicKey, "Base64-encoded ed25519 public key for update manifest verification")
	fs.StringVar(&cfg.AutoUpdateMode, "auto-update", "check", "Auto-update mode: off, check, apply")
	fs.DurationVar(&cfg.UpdateCheckInterval, "update-check-interval", 6*time.Hour, "Interval between update checks")
	if err := fs.Parse(args); err != nil {
		return trayConfig{}, err
	}
	return cfg, nil
}

func (a *trayApp) onReady() {
	systray.SetIcon(trayicon.ProxyICO())
	systray.SetTitle("Brainhub")
	systray.SetTooltip("Brainhub proxy tray")

	a.statusItem = systray.AddMenuItem("Starting proxy...", "Current proxy status")
	a.statusItem.Disable()
	a.loginItem = systray.AddMenuItem("Start login challenge", "Start OAuth login in the browser")
	a.copyLoginItem = systray.AddMenuItem("Copy login link", "Copy the current login URL to clipboard")
	systray.AddSeparator()
	a.authItem = systray.AddMenuItem("Open auth folder", "Open the .brainhub folder")
	a.logItem = systray.AddMenuItem("Open tray log", "Open the tray log file")
	systray.AddSeparator()
	a.checkUpdatesItem = systray.AddMenuItem("Check for updates", "Check release manifest for a newer version")
	a.installUpdateItem = systray.AddMenuItem("Install update", "Download the latest version and restart")
	a.installUpdateItem.Disable()
	systray.AddSeparator()
	a.quitItem = systray.AddMenuItem("Quit", "Stop proxy and exit")

	logger, err := newTrayLogger(a.cfg.LogFile)
	if err != nil {
		a.setStatus("Tray init error: " + err.Error())
		return
	}
	a.logger = logger
	a.logger.Info("build info", zap.String("build", buildinfo.Summary()))

	go a.handleMenu()
	go a.startProxy()
	go a.startUpdateLoop()
}

func (a *trayApp) onExit() {
	a.shutdown()
}

func (a *trayApp) handleMenu() {
	for {
		select {
		case <-a.loginItem.ClickedCh:
			go a.triggerLoginChallenge()
		case <-a.copyLoginItem.ClickedCh:
			go a.copyLoginLink()
		case <-a.authItem.ClickedCh:
			a.openPath(filepath.Dir(a.cfg.AuthFile))
		case <-a.logItem.ClickedCh:
			a.openPath(a.cfg.LogFile)
		case <-a.checkUpdatesItem.ClickedCh:
			go a.checkForUpdates(false)
		case <-a.installUpdateItem.ClickedCh:
			go a.installAvailableUpdate()
		case <-a.quitItem.ClickedCh:
			systray.Quit()
			return
		}
	}
}

func (a *trayApp) startProxy() {
	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
		return
	}
	old := a.proxy
	a.proxy = nil
	a.mu.Unlock()

	if old != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = old.Stop(ctx)
		cancel()
	}

	app, err := proxyapp.New(proxyapp.Config{
		Listen:       a.cfg.Listen,
		AuthFile:     a.cfg.AuthFile,
		Upstream:     a.cfg.Upstream,
		ReadTimeout:  a.cfg.ReadTimeout,
		WriteTimeout: a.cfg.WriteTimeout,
		ServiceName:  a.cfg.ServiceName,
		Logger:       a.logger,
	})
	if err != nil {
		a.logger.Error("create proxy app", zap.Error(err))
		a.setStatus("Proxy error: " + err.Error())
		return
	}
	if err := app.Start(); err != nil {
		a.logger.Error("start proxy app", zap.Error(err))
		a.setStatus("Proxy error: " + err.Error())
		return
	}

	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = app.Stop(ctx)
		cancel()
		return
	}
	a.proxy = app
	a.mu.Unlock()
	a.setStatus("Proxy running on http://" + app.Address())

	if err := app.Wait(); err != nil {
		a.logger.Error("proxy app stopped with error", zap.Error(err))
		a.clearCurrentProxy(app)
		a.setStatus("Proxy stopped: " + err.Error())
		return
	}
	a.clearCurrentProxy(app)
	a.setStatus("Proxy stopped")
}

func (a *trayApp) shutdown() {
	a.stopOnce.Do(func() {
		a.mu.Lock()
		a.stopped = true
		proxy := a.proxy
		login := a.login
		a.mu.Unlock()

		if login != nil && login.cancel != nil {
			login.cancel()
		}
		if proxy != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = proxy.Stop(ctx)
		}
		if a.logger != nil {
			_ = a.logger.Sync()
		}
	})
}

func (a *trayApp) setStatus(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		text = "Brainhub"
	}
	systray.SetTooltip(text)
	if a.statusItem != nil {
		a.statusItem.SetTitle(text)
	}
}

func (a *trayApp) clearCurrentProxy(app *proxyapp.App) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.proxy == app {
		a.proxy = nil
	}
}

func (a *trayApp) triggerLoginChallenge() {
	session, started := a.ensureLoginSession(false)
	if session == nil {
		return
	}
	if started {
		a.setStatus("Login challenge started")
		return
	}
	if url, ok := a.waitForLoginURL(session, 2*time.Second); ok {
		if err := openURL(url); err != nil {
			a.logger.Error("open login url", zap.String("url", url), zap.Error(err))
			a.setStatus("Login URL open failed")
			return
		}
		a.setStatus("Login challenge opened")
		return
	}
	a.setStatus("Login challenge is starting")
}

func (a *trayApp) copyLoginLink() {
	session, _ := a.ensureLoginSession(true)
	if session == nil {
		return
	}
	url, ok := a.waitForLoginURL(session, 5*time.Second)
	if !ok {
		a.setStatus("Login link is not ready yet")
		return
	}
	if err := copyToClipboard(url); err != nil {
		a.logger.Error("copy login url", zap.String("url", url), zap.Error(err))
		a.setStatus("Copy login link failed")
		return
	}
	a.setStatus("Login link copied to clipboard")
}

func (a *trayApp) ensureLoginSession(noBrowser bool) (*loginSession, bool) {
	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
		return nil, false
	}
	if a.login != nil {
		session := a.login
		a.mu.Unlock()
		return session, false
	}

	ctx, cancel := context.WithCancel(context.Background())
	session := &loginSession{
		cancel:   cancel,
		done:     make(chan struct{}),
		urlReady: make(chan struct{}),
	}
	a.login = session
	a.mu.Unlock()

	go a.runLoginSession(ctx, session, noBrowser)
	return session, true
}

func (a *trayApp) runLoginSession(ctx context.Context, session *loginSession, noBrowser bool) {
	defer close(session.done)

	writer := &trayLoginWriter{app: a, session: session}
	code, err := oauth.RunLogin(ctx, oauth.LoginOptions{
		NoBrowser: noBrowser,
		Verbose:   true,
		AuthFile:  a.cfg.AuthFile,
		HomeDir:   filepath.Dir(a.cfg.AuthFile),
		Stdin:     strings.NewReader(""),
		Stderr:    writer,
	})

	a.mu.Lock()
	if a.login == session {
		a.login = nil
	}
	a.mu.Unlock()

	if err != nil {
		a.logger.Error("login flow failed", zap.Int("code", code), zap.Error(err))
		a.setStatus("Login failed: " + err.Error())
		return
	}
	if code != 0 {
		a.setStatus(fmt.Sprintf("Login stopped with code %d", code))
		return
	}

	a.setStatus("Login successful; reloading proxy...")
	go a.startProxy()
}

func (a *trayApp) waitForLoginURL(session *loginSession, timeout time.Duration) (string, bool) {
	a.mu.Lock()
	if session.url != "" {
		url := session.url
		a.mu.Unlock()
		return url, true
	}
	urlReady := session.urlReady
	done := session.done
	a.mu.Unlock()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-urlReady:
	case <-done:
	case <-timer.C:
		return "", false
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	return session.url, session.url != ""
}

type trayLoginWriter struct {
	app     *trayApp
	session *loginSession

	mu      sync.Mutex
	pending string
}

func (w *trayLoginWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.pending += string(p)
	for {
		idx := strings.IndexByte(w.pending, '\n')
		if idx < 0 {
			break
		}
		line := strings.TrimSpace(w.pending[:idx])
		w.pending = w.pending[idx+1:]
		w.handleLine(line)
	}
	return len(p), nil
}

func (w *trayLoginWriter) handleLine(line string) {
	if line == "" {
		return
	}
	w.app.logger.Info("login", zap.String("line", line))
	if strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://") {
		w.app.mu.Lock()
		if w.session.url == "" {
			w.session.url = line
			close(w.session.urlReady)
		}
		w.app.mu.Unlock()
	}
}

func copyToClipboard(text string) error {
	cmd := exec.Command("cmd", "/c", "clip")
	cmd.Stdin = bytes.NewBufferString(text)
	return cmd.Run()
}

func openURL(rawURL string) error {
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL)
	return cmd.Start()
}

func (a *trayApp) openPath(path string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	cmd := exec.Command("explorer.exe", path)
	if err := cmd.Start(); err != nil && a.logger != nil {
		a.logger.Error("open path", zap.String("path", path), zap.Error(err))
	}
}

func newTrayLogger(path string) (*zap.Logger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	cfg := zap.NewProductionConfig()
	cfg.OutputPaths = []string{path}
	cfg.ErrorOutputPaths = []string{path}
	return cfg.Build()
}
