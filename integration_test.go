package main

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newIntegrationHandler(t *testing.T, larkURL string, logs *bytes.Buffer) *httptest.Server {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(logs, nil))
	h := newHandler(
		config{webhookURL: validWebhookURL, token: testToken, port: "5000"},
		newLarkClient(larkURL, defaultHTTPClient()),
		logger,
	)
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	return server
}

func postWebhook(t *testing.T, url, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url+"/webhook", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post webhook: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func cardNoteContainsKeyword(t *testing.T, raw []byte) bool {
	t.Helper()
	decoded := decodeCardMessage(t, raw)
	card, ok := decoded["card"].(map[string]any)
	if !ok {
		return false
	}
	elements, ok := card["elements"].([]any)
	if !ok {
		return false
	}
	for _, element := range elements {
		item, ok := element.(map[string]any)
		if !ok || item["tag"] != "note" {
			continue
		}
		noteElements, ok := item["elements"].([]any)
		if !ok {
			continue
		}
		for _, noteElement := range noteElements {
			note, ok := noteElement.(map[string]any)
			if !ok {
				continue
			}
			content, _ := note["content"].(string)
			if strings.Contains(content, "Prometheus告警") {
				return true
			}
		}
	}
	return false
}

func TestIntegrationSuccess(t *testing.T) {
	var received []byte
	lark := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = io.WriteString(w, `{"code":0,"msg":"success"}`)
	}))
	t.Cleanup(lark.Close)

	var logs bytes.Buffer
	adapter := newIntegrationHandler(t, lark.URL, &logs)

	resp := postWebhook(t, adapter.URL, sampleJSON)
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", resp.StatusCode, body)
	}

	decoded := decodeCardMessage(t, received)
	if decoded["msg_type"] != "interactive" {
		t.Fatalf("msg_type = %v, want interactive", decoded["msg_type"])
	}
	card, ok := decoded["card"].(map[string]any)
	if !ok {
		t.Fatalf("card = %T, want object", decoded["card"])
	}
	if _, exists := card["schema"]; exists {
		t.Fatal("card must not contain schema 2.0 field")
	}
	if !strings.Contains(string(received), "Prometheus告警") {
		t.Fatal("card must contain the keyword Prometheus告警")
	}
	if !cardNoteContainsKeyword(t, received) {
		t.Fatal("card note must contain the keyword Prometheus告警")
	}
	if strings.Contains(logs.String(), testToken) {
		t.Fatal("logs must not contain the webhook token")
	}
	if strings.Contains(logs.String(), lark.URL) {
		t.Fatal("logs must not contain the Lark URL")
	}
}

func TestIntegrationLarkKeywordError(t *testing.T) {
	lark := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = io.WriteString(w, `{"code":19024,"msg":"Key Words Not Found"}`)
	}))
	t.Cleanup(lark.Close)

	var logs bytes.Buffer
	adapter := newIntegrationHandler(t, lark.URL, &logs)

	resp := postWebhook(t, adapter.URL, sampleJSON)
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}

	logOutput := logs.String()
	if !strings.Contains(logOutput, `"lark_code":19024`) {
		t.Fatalf("logs must contain lark code 19024, got %s", logOutput)
	}
	if !strings.Contains(logOutput, "Key Words Not Found") {
		t.Fatalf("logs must contain the Lark message, got %s", logOutput)
	}
	for _, secret := range []string{testToken, lark.URL, validWebhookURL, "CPU usage is above 90% for 5 minutes", "HighCPU"} {
		if strings.Contains(logOutput, secret) {
			t.Fatalf("logs must not contain %q, got %s", secret, logOutput)
		}
	}
	for _, secret := range []string{testToken, lark.URL} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("response must not contain %q, got %s", secret, body)
		}
	}
}

func TestIntegrationHealthz(t *testing.T) {
	var logs bytes.Buffer
	adapter := newIntegrationHandler(t, unusedLarkURL, &logs)

	resp, err := http.Get(adapter.URL + "/healthz")
	if err != nil {
		t.Fatalf("get healthz: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}
