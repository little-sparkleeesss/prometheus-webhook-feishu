package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	defaultPort    = "5000"
	listenAddr     = "0.0.0.0"
	hookPathPrefix = "/open-apis/bot/v2/hook/"
)

type config struct {
	webhookURL string
	token      string
	port       string
}

var allowedWebhookHosts = map[string]struct{}{
	"open.feishu.cn":     {},
	"open.larksuite.com": {},
}

func validateWebhookURL(raw string) error {
	if raw == "" {
		return errors.New("FEISHU_WEBHOOK_URL is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return errors.New("FEISHU_WEBHOOK_URL is not a valid URL")
	}
	if parsed.Scheme != "https" {
		return errors.New("FEISHU_WEBHOOK_URL must use https")
	}
	if parsed.User != nil {
		return errors.New("FEISHU_WEBHOOK_URL must not contain credentials")
	}
	if parsed.Port() != "" {
		return errors.New("FEISHU_WEBHOOK_URL must not contain a port")
	}
	if _, ok := allowedWebhookHosts[strings.ToLower(parsed.Hostname())]; !ok {
		return errors.New("FEISHU_WEBHOOK_URL host must be open.feishu.cn or open.larksuite.com")
	}
	if !strings.HasPrefix(parsed.Path, hookPathPrefix) || len(parsed.Path) == len(hookPathPrefix) {
		return errors.New("FEISHU_WEBHOOK_URL path must start with " + hookPathPrefix)
	}
	return nil
}

func loadConfig(getenv func(string) string) (config, error) {
	webhookURL := getenv("FEISHU_WEBHOOK_URL")
	if err := validateWebhookURL(webhookURL); err != nil {
		return config{}, err
	}
	token := getenv("WEBHOOK_TOKEN")
	if token == "" || token == "<no value>" {
		return config{}, errors.New("WEBHOOK_TOKEN is required")
	}
	port := getenv("PORT")
	if port == "" {
		port = defaultPort
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return config{}, errors.New("PORT must be an integer between 1 and 65535")
	}
	return config{
		webhookURL: webhookURL,
		token:      token,
		port:       strconv.Itoa(portNumber),
	}, nil
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		logger.Error("invalid configuration", "event", "startup_failed", "reason", err.Error())
		os.Exit(1)
	}
	server := &http.Server{
		Addr:              listenAddr + ":" + cfg.port,
		Handler:           newHandler(cfg, newLarkClient(cfg.webhookURL, defaultHTTPClient()), logger),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       requestReadTimeout,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("server started", "event", "startup", "port", cfg.port, "auth_enabled", cfg.token != "")
		serveErr <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown requested", "event", "shutdown")
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server error", "event", "server_failed", "error", err.Error())
			os.Exit(1)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "event", "shutdown_failed", "error", err.Error())
		os.Exit(1)
	}
	logger.Info("server stopped", "event", "shutdown_complete")
}
