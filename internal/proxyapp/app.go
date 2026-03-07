package proxyapp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"brainhub/internal/proxy"

	"go.uber.org/zap"
)

type Config struct {
	Listen       string
	AuthFile     string
	Upstream     string
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	ServiceName  string
	Logger       *zap.Logger
}

type App struct {
	server   *http.Server
	listener net.Listener
	logger   *zap.Logger
	authFile string
	upstream string

	mu    sync.Mutex
	errCh chan error
}

func New(cfg Config) (*App, error) {
	listen := strings.TrimSpace(cfg.Listen)
	if listen == "" {
		listen = ":8080"
	}

	authFile := strings.TrimSpace(cfg.AuthFile)
	if authFile == "" {
		authFile = proxy.DefaultAuthFilePath()
	}

	upstream := strings.TrimSpace(cfg.Upstream)
	if upstream == "" {
		upstream = "https://chatgpt.com"
	}

	serviceName := strings.TrimSpace(cfg.ServiceName)
	if serviceName == "" {
		serviceName = "brainhub-proxy"
	}

	logger := cfg.Logger
	if logger == nil {
		var err error
		logger, err = zap.NewDevelopment()
		if err != nil {
			return nil, fmt.Errorf("create logger: %w", err)
		}
	}

	tokens, err := proxy.LoadAuthTokens(authFile)
	if err != nil {
		return nil, err
	}

	handler, err := proxy.NewServer(proxy.Config{
		AccessToken: tokens.AccessToken,
		AccountID:   tokens.AccountID,
		BaseURL:     upstream,
		ServiceName: serviceName,
	}, logger)
	if err != nil {
		return nil, err
	}

	return &App{
		server: &http.Server{
			Addr:         listen,
			Handler:      handler,
			ReadTimeout:  cfg.ReadTimeout,
			WriteTimeout: cfg.WriteTimeout,
		},
		logger:   logger,
		authFile: authFile,
		upstream: upstream,
	}, nil
}

func (a *App) Start() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.listener != nil {
		return fmt.Errorf("proxy app already started")
	}

	ln, err := net.Listen("tcp", a.server.Addr)
	if err != nil {
		return err
	}
	a.listener = ln
	a.errCh = make(chan error, 1)
	go func() {
		a.errCh <- a.server.Serve(ln)
	}()
	return nil
}

func (a *App) Wait() error {
	a.mu.Lock()
	errCh := a.errCh
	a.mu.Unlock()
	if errCh == nil {
		return fmt.Errorf("proxy app is not running")
	}

	err := <-errCh
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (a *App) Stop(ctx context.Context) error {
	return a.server.Shutdown(ctx)
}

func (a *App) Address() string {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.listener != nil {
		return a.listener.Addr().String()
	}
	return a.server.Addr
}

func (a *App) AuthFile() string {
	return a.authFile
}

func (a *App) Upstream() string {
	return a.upstream
}

func (a *App) Logger() *zap.Logger {
	return a.logger
}
