package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"brainhub/internal/buildinfo"
	"brainhub/internal/proxy"
	"brainhub/internal/proxyapp"

	"go.uber.org/zap"
)

func main() {
	fs := flag.NewFlagSet("proxy", flag.ExitOnError)
	listen := fs.String("listen", ":8080", "HTTP listen address")
	authFile := fs.String("auth-file", proxy.DefaultAuthFilePath(), "Path to auth.json")
	upstream := fs.String("upstream", "https://chatgpt.com", "Codex upstream base URL")
	readTimeout := fs.Duration("read-timeout", 30*time.Second, "HTTP server read timeout")
	writeTimeout := fs.Duration("write-timeout", 0, "HTTP server write timeout (0 disables; recommended for streaming)")
	serviceName := fs.String("service-name", "brainhub-proxy", "Service name for structured logs")
	updateManifestURL := fs.String("update-manifest-url", "", "Release manifest URL for update checks")
	updateChannel := fs.String("update-channel", buildinfo.Channel, "Release channel for update checks")
	updatePublicKey := fs.String("update-public-key", buildinfo.UpdatePublicKey, "Base64-encoded ed25519 public key for update manifest verification")
	autoUpdate := fs.String("auto-update", "off", "Auto-update mode: off, check, apply")
	updateCheckInterval := fs.Duration("update-check-interval", 6*time.Hour, "Interval between update checks")
	windowsServiceName := fs.String("windows-service-name", "", "Windows service name used by updater when installed as a service")
	if err := fs.Parse(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(2)
	}

	logger, err := zap.NewDevelopment()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR: create logger:", err)
		os.Exit(1)
	}
	defer func() { _ = logger.Sync() }()
	logger.Info("build info", zap.String("build", buildinfo.Summary()))

	app, err := proxyapp.New(proxyapp.Config{
		Listen:       *listen,
		AuthFile:     *authFile,
		Upstream:     *upstream,
		ReadTimeout:  *readTimeout,
		WriteTimeout: *writeTimeout,
		ServiceName:  *serviceName,
		Logger:       logger,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}

	if err := app.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "proxy listening on %s\n", app.Address())
	fmt.Fprintf(os.Stderr, "using auth file: %s\n", app.AuthFile())
	fmt.Fprintf(os.Stderr, "upstream: %s\n", app.Upstream())
	startProxyAutoUpdateLoop(app, logger, proxyUpdateConfig{
		ManifestURL:        *updateManifestURL,
		Channel:            *updateChannel,
		PublicKey:          *updatePublicKey,
		Mode:               *autoUpdate,
		CheckInterval:      *updateCheckInterval,
		WindowsServiceName: *windowsServiceName,
	})
	if err := app.Wait(); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
}
