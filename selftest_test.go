package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestSelfTestPayload(t *testing.T) {
	payload := selfTestPayload()

	if payload.Status != "firing" {
		t.Fatalf("status = %q, want firing", payload.Status)
	}
	if len(payload.Alerts) != 1 {
		t.Fatalf("alerts = %d, want 1", len(payload.Alerts))
	}
	item := payload.Alerts[0]
	if item.Labels["alertname"] != "LinkTest" {
		t.Fatalf("alertname = %q, want LinkTest", item.Labels["alertname"])
	}
	if item.Labels["severity"] != "info" {
		t.Fatalf("severity = %q, want info", item.Labels["severity"])
	}
	if payload.CommonLabels["cluster"] != "self-test" {
		t.Fatalf("cluster = %q, want self-test", payload.CommonLabels["cluster"])
	}
	if _, err := time.Parse(time.RFC3339, item.StartsAt); err != nil {
		t.Fatalf("startsAt = %q is not RFC3339: %v", item.StartsAt, err)
	}
}

func selfTestEnv(port string) func(string) string {
	env := map[string]string{
		"FEISHU_WEBHOOK_URL": validWebhookURL,
		"WEBHOOK_TOKEN":      testToken,
		"PORT":               port,
	}
	return func(key string) string { return env[key] }
}

func selfTestPort(t *testing.T, raw string) string {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	port := parsed.Port()
	if port == "" {
		t.Fatal("server URL has no port")
	}
	return port
}

func TestSelfTestSuccess(t *testing.T) {
	type capture struct {
		authorization string
		contentType   string
		contentLength int64
		body          []byte
	}
	received := make(chan capture, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received <- capture{
			authorization: r.Header.Get("Authorization"),
			contentType:   r.Header.Get("Content-Type"),
			contentLength: r.ContentLength,
			body:          body,
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	}))
	t.Cleanup(server.Close)

	var stdout bytes.Buffer
	code := runSelfTest(selfTestEnv(selfTestPort(t, server.URL)), &stdout, &http.Client{Timeout: 5 * time.Second})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; output: %s", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), "self-test passed") {
		t.Fatalf("output = %q, want self-test passed", stdout.String())
	}
	if strings.Contains(stdout.String(), testToken) || strings.Contains(stdout.String(), validWebhookURL) {
		t.Fatalf("output must not contain secrets: %s", stdout.String())
	}

	var got capture
	select {
	case got = <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("local webhook did not receive a request")
	}
	if got.authorization != "Bearer "+testToken {
		t.Fatalf("Authorization = %q, want Bearer token", got.authorization)
	}
	if got.contentType != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got.contentType)
	}
	if got.contentLength != int64(len(got.body)) {
		t.Fatalf("Content-Length = %d, body length = %d", got.contentLength, len(got.body))
	}

	var payload alertmanagerPayload
	if err := json.Unmarshal(got.body, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Status != "firing" {
		t.Fatalf("status = %q, want firing", payload.Status)
	}
	if len(payload.Alerts) != 1 {
		t.Fatalf("alerts = %d, want 1", len(payload.Alerts))
	}
	if payload.Alerts[0].Labels["alertname"] != "LinkTest" {
		t.Fatalf("alertname = %q, want LinkTest", payload.Alerts[0].Labels["alertname"])
	}
	if payload.CommonLabels["cluster"] != "self-test" {
		t.Fatalf("cluster = %q, want self-test", payload.CommonLabels["cluster"])
	}
}

func TestSelfTestHTTPFailure(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusBadGateway} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"error":"test failure"}`)
			}))
			t.Cleanup(server.Close)

			var stdout bytes.Buffer
			code := runSelfTest(selfTestEnv(selfTestPort(t, server.URL)), &stdout, &http.Client{Timeout: 5 * time.Second})

			if code != 1 {
				t.Fatalf("exit code = %d, want 1; output: %s", code, stdout.String())
			}
			if !strings.Contains(stdout.String(), fmt.Sprintf("HTTP %d", status)) {
				t.Fatalf("output = %q, want HTTP %d", stdout.String(), status)
			}
			if strings.Contains(stdout.String(), testToken) || strings.Contains(stdout.String(), validWebhookURL) {
				t.Fatalf("output must not contain secrets: %s", stdout.String())
			}
		})
	}
}

func TestSelfTestInvalidConfig(t *testing.T) {
	var stdout bytes.Buffer
	code := runSelfTest(func(string) string { return "" }, &stdout, &http.Client{Timeout: time.Second})

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stdout.String(), "invalid configuration") {
		t.Fatalf("output = %q, want invalid configuration", stdout.String())
	}
}

func TestSelfTestConnectionRefused(t *testing.T) {
	var stdout bytes.Buffer
	code := runSelfTest(selfTestEnv(freePort(t)), &stdout, &http.Client{Timeout: 2 * time.Second})

	if code != 1 {
		t.Fatalf("exit code = %d, want 1; output: %s", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), "local webhook request") {
		t.Fatalf("output = %q, want local webhook request error", stdout.String())
	}
}

func runSelfTestBinary(t *testing.T, port string) (string, error) {
	t.Helper()
	binary := buildTestBinary(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "--self-test")
	cmd.Env = []string{
		"FEISHU_WEBHOOK_URL=" + validWebhookURL,
		"WEBHOOK_TOKEN=" + testToken,
		"PORT=" + port,
	}
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output

	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("self-test mode did not exit; output: %s", output.String())
	}
	return output.String(), err
}

func TestMainSelfTestMode(t *testing.T) {
	received := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		received <- r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	}))
	t.Cleanup(server.Close)

	output, err := runSelfTestBinary(t, selfTestPort(t, server.URL))
	if err != nil {
		t.Fatalf("run = %v; output: %s", err, output)
	}
	if !strings.Contains(output, "self-test passed") {
		t.Fatalf("output = %q, want self-test passed", output)
	}
	if strings.Contains(output, testToken) || strings.Contains(output, validWebhookURL) {
		t.Fatalf("output must not contain secrets: %s", output)
	}

	select {
	case authorization := <-received:
		if authorization != "Bearer "+testToken {
			t.Fatalf("Authorization = %q, want Bearer token", authorization)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("local webhook did not receive a request")
	}
}

func TestMainSelfTestModeFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"error":"failed to send notification"}`)
	}))
	t.Cleanup(server.Close)

	output, err := runSelfTestBinary(t, selfTestPort(t, server.URL))
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("run = %v, want a non-zero exit; output: %s", err, output)
	}
	if exitErr.ExitCode() != 1 {
		t.Fatalf("exit code = %d, want 1", exitErr.ExitCode())
	}
	if !strings.Contains(output, "HTTP 502") {
		t.Fatalf("output = %q, want HTTP 502", output)
	}
	if strings.Contains(output, testToken) || strings.Contains(output, validWebhookURL) {
		t.Fatalf("output must not contain secrets: %s", output)
	}
}
