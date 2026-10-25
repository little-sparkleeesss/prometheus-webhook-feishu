package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	testToken     = "test-secret-token"
	sampleJSON    = `{"status":"firing","alerts":[{"status":"firing","labels":{"alertname":"HighCPU","severity":"critical","instance":"10.0.0.1:9100"},"annotations":{"summary":"CPU high","description":"CPU above 90%"},"startsAt":"2026-10-24T12:00:00Z"}],"commonLabels":{"cluster":"prod"},"truncatedAlerts":0}`
	unusedLarkURL = "http://127.0.0.1:1"
)

func newTestHandler(t *testing.T, token, larkURL string) *handler {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	return newHandler(
		config{webhookURL: validWebhookURL, token: token, port: "5000"},
		newLarkClient(larkURL, defaultHTTPClient()),
		logger,
	)
}

func newLarkOKServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = io.WriteString(w, `{"code":0,"msg":"success"}`)
	}))
	t.Cleanup(server.Close)
	return server
}

func doRequest(t *testing.T, h *handler, method, path, body, authorization string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, req)
	return recorder
}

func TestHandlerMethodNotAllowed(t *testing.T) {
	h := newTestHandler(t, "", unusedLarkURL)
	recorder := doRequest(t, h, http.MethodGet, "/webhook", "", "")

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", recorder.Code)
	}
	if allow := recorder.Header().Get("Allow"); allow != http.MethodPost {
		t.Fatalf("Allow = %q, want POST", allow)
	}
}

func TestHandlerMissingContentLength(t *testing.T) {
	h := newTestHandler(t, "", unusedLarkURL)
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(sampleJSON))
	req.ContentLength = -1
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}

func TestHandlerBodyTooLarge(t *testing.T) {
	h := newTestHandler(t, "", unusedLarkURL)
	body := strings.Repeat("a", maxBodyBytes+1)
	recorder := doRequest(t, h, http.MethodPost, "/webhook", body, "")

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", recorder.Code)
	}
}

func TestHandlerBodyTooLargeWithoutContentLength(t *testing.T) {
	h := newTestHandler(t, testToken, unusedLarkURL)
	body := strings.Repeat("a", maxBodyBytes+1)
	req := httptest.NewRequest(http.MethodPost, "/webhook", nil)
	req.Body = io.NopCloser(bytes.NewReader([]byte(body)))
	req.ContentLength = 10
	req.Header.Set("Authorization", "Bearer "+testToken)
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", recorder.Code)
	}
}

func TestHandlerBearerToken(t *testing.T) {
	lark := newLarkOKServer(t)
	h := newTestHandler(t, testToken, lark.URL)

	cases := []struct {
		name          string
		authorization string
		wantStatus    int
	}{
		{"missing", "", http.StatusUnauthorized},
		{"wrong token", "Bearer wrong-token", http.StatusUnauthorized},
		{"wrong scheme", "Basic " + testToken, http.StatusUnauthorized},
		{"correct", "Bearer " + testToken, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := doRequest(t, h, http.MethodPost, "/webhook", sampleJSON, tc.authorization)
			if recorder.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, tc.wantStatus)
			}
		})
	}
}

func TestHandlerInvalidPayloads(t *testing.T) {
	h := newTestHandler(t, testToken, unusedLarkURL)
	cases := map[string]string{
		"invalid json":         `not json`,
		"missing alerts":       `{"status":"firing"}`,
		"missing status":       `{"alerts":[]}`,
		"status wrong type":    `{"status":123,"alerts":[]}`,
		"alerts wrong type":    `{"status":"firing","alerts":{}}`,
		"truncated wrong type": `{"status":"firing","alerts":[],"truncatedAlerts":"x"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			recorder := doRequest(t, h, http.MethodPost, "/webhook", body, "Bearer "+testToken)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", recorder.Code)
			}
		})
	}
}

func TestHandlerConcurrencyLimit(t *testing.T) {
	lark := newLarkOKServer(t)
	h := newTestHandler(t, testToken, lark.URL)
	for i := 0; i < maxConcurrentRequests; i++ {
		h.sem <- struct{}{}
	}
	defer func() {
		for i := 0; i < maxConcurrentRequests; i++ {
			<-h.sem
		}
	}()

	recorder := doRequest(t, h, http.MethodPost, "/webhook", sampleJSON, "Bearer "+testToken)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
}

func TestHandlerSuccessResponse(t *testing.T) {
	lark := newLarkOKServer(t)
	h := newTestHandler(t, testToken, lark.URL)

	recorder := doRequest(t, h, http.MethodPost, "/webhook", sampleJSON, "Bearer "+testToken)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("body = %v, want status ok", body)
	}
}

func TestHandlerLarkFailure(t *testing.T) {
	lark := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = io.WriteString(w, `{"code":19024,"msg":"Key Words Not Found"}`)
	}))
	t.Cleanup(lark.Close)
	h := newTestHandler(t, testToken, lark.URL)

	recorder := doRequest(t, h, http.MethodPost, "/webhook", sampleJSON, "Bearer "+testToken)

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), lark.URL) {
		t.Fatal("response must not contain the Lark URL")
	}
}

func TestHandlerEmptyTokenDeniesRequests(t *testing.T) {
	h := newTestHandler(t, "", unusedLarkURL)

	for _, authorization := range []string{"", "Bearer anything"} {
		recorder := doRequest(t, h, http.MethodPost, "/webhook", sampleJSON, authorization)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("status with authorization %q = %d, want 401", authorization, recorder.Code)
		}
	}
}

func TestHandlerHealthz(t *testing.T) {
	h := newTestHandler(t, "", unusedLarkURL)

	recorder := doRequest(t, h, http.MethodGet, "/healthz", "", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}

	recorder = doRequest(t, h, http.MethodPost, "/healthz", "", "")
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /healthz status = %d, want 405", recorder.Code)
	}

	h.cfg.webhookURL = "http://open.feishu.cn/open-apis/bot/v2/hook/abc"
	recorder = doRequest(t, h, http.MethodGet, "/healthz", "", "")
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("invalid config status = %d, want 500", recorder.Code)
	}
}

func TestHandlerUnknownPath(t *testing.T) {
	h := newTestHandler(t, "", unusedLarkURL)
	recorder := doRequest(t, h, http.MethodGet, "/other", "", "")

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
}
