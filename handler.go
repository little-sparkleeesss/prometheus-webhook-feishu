package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const (
	maxBodyBytes          = 1 << 20
	maxConcurrentRequests = 10
	requestReadTimeout    = 10 * time.Second
	maxClusterLogRunes    = 128
	maxStatusLogRunes     = 64
	authorizationScheme   = "Bearer "
	webhookPath           = "/webhook"
	healthPath            = "/healthz"
)

type handler struct {
	cfg    config
	lark   *larkClient
	logger *slog.Logger
	sem    chan struct{}
}

func newHandler(cfg config, lark *larkClient, logger *slog.Logger) *handler {
	return &handler{
		cfg:    cfg,
		lark:   lark,
		logger: logger,
		sem:    make(chan struct{}, maxConcurrentRequests),
	}
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case webhookPath:
		h.handleWebhook(w, r)
	case healthPath:
		h.handleHealthz(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *handler) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		h.reject(w, http.StatusMethodNotAllowed, "method not allowed", "method_not_allowed")
		return
	}
	if r.ContentLength < 0 {
		h.reject(w, http.StatusBadRequest, "content-length is required", "missing_content_length")
		return
	}
	if r.ContentLength > maxBodyBytes {
		h.reject(w, http.StatusRequestEntityTooLarge, "request body too large", "body_too_large")
		return
	}
	if !h.authorized(r) {
		h.reject(w, http.StatusUnauthorized, "unauthorized", "unauthorized")
		return
	}

	select {
	case h.sem <- struct{}{}:
		defer func() { <-h.sem }()
	default:
		h.reject(w, http.StatusServiceUnavailable, "too many concurrent requests", "concurrency_limit")
		return
	}

	// Enforce a per-request read deadline in addition to the server defaults.
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(requestReadTimeout))

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			h.reject(w, http.StatusRequestEntityTooLarge, "request body too large", "body_too_large")
			return
		}
		h.reject(w, http.StatusBadRequest, "failed to read request body", "body_read_error")
		return
	}

	var payload alertmanagerPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		h.reject(w, http.StatusBadRequest, "invalid JSON payload", "invalid_json")
		return
	}
	if err := payload.validate(); err != nil {
		h.reject(w, http.StatusBadRequest, "invalid payload: "+err.Error(), "invalid_payload")
		return
	}

	started := time.Now()
	sendErr := h.lark.send(r.Context(), buildCardMessage(payload))
	elapsed := time.Since(started)

	attrs := []any{
		"event", "webhook_processed",
		"alerts", len(payload.Alerts),
		"status", truncateRunes(payload.Status, maxStatusLogRunes),
		"cluster", logCluster(payload),
		"duration_ms", elapsed.Milliseconds(),
	}
	if sendErr != nil {
		var larkErr *larkError
		if errors.As(sendErr, &larkErr) {
			attrs = append(attrs,
				"lark_code", larkErr.code,
				"http_status", larkErr.httpStatus,
				"error_kind", larkErr.kind,
			)
			if larkErr.msg != "" {
				attrs = append(attrs, "lark_msg", truncateRunes(h.redactLarkMessage(larkErr.msg), maxLarkMessageLogSize))
			}
		}
		h.logger.Error("failed to forward alert", attrs...)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to send notification"})
		return
	}

	h.logger.Info("alert forwarded", attrs...)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *handler) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if err := validateWebhookURL(h.cfg.webhookURL); err != nil {
		writeError(w, http.StatusInternalServerError, "invalid configuration")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *handler) reject(w http.ResponseWriter, status int, message, reason string) {
	h.logger.Warn("webhook rejected", "event", "webhook_rejected", "reason", reason)
	writeError(w, status, message)
}

func (h *handler) authorized(r *http.Request) bool {
	if h.cfg.token == "" {
		return false
	}
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, authorizationScheme) {
		return false
	}
	provided := strings.TrimPrefix(header, authorizationScheme)
	return secureEqual(provided, h.cfg.token)
}

// secureEqual hashes both inputs first so comparison time does not depend on
// the input length.
func secureEqual(a, b string) bool {
	left := sha256.Sum256([]byte(a))
	right := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(left[:], right[:]) == 1
}

// redactLarkMessage removes local secrets that an upstream error message may
// echo before the message is written to logs.
func (h *handler) redactLarkMessage(msg string) string {
	secrets := []string{h.cfg.webhookURL, h.cfg.token, webhookKey(h.cfg.webhookURL)}
	seen := make(map[string]struct{}, len(secrets))
	pairs := make([]string, 0, len(secrets)*2)
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		if _, ok := seen[secret]; ok {
			continue
		}
		seen[secret] = struct{}{}
		pairs = append(pairs, secret, "[redacted]")
	}
	if len(pairs) == 0 {
		return msg
	}
	return strings.NewReplacer(pairs...).Replace(msg)
}

func logCluster(p alertmanagerPayload) string {
	value := cleanValue(p.CommonLabels["cluster"])
	if value == "" && len(p.Alerts) > 0 {
		value = cleanValue(p.Alerts[0].Labels["cluster"])
	}
	if value == "" {
		return "-"
	}
	return truncateRunes(value, maxClusterLogRunes)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
