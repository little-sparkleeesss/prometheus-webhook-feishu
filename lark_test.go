package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func decodeCardMessage(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode card message: %v", err)
	}
	return decoded
}

func TestLarkSendSuccess(t *testing.T) {
	var (
		receivedContentType string
		receivedBody        []byte
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedContentType = r.Header.Get("Content-Type")
		receivedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = io.WriteString(w, `{"code":0,"msg":"success"}`)
	}))
	t.Cleanup(server.Close)

	client := newLarkClient(server.URL, defaultHTTPClient())
	if err := client.send(context.Background(), buildCardMessage(samplePayload())); err != nil {
		t.Fatalf("send: %v", err)
	}
	if receivedContentType != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", receivedContentType)
	}

	decoded := decodeCardMessage(t, receivedBody)
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
	header, ok := card["header"].(map[string]any)
	if !ok {
		t.Fatalf("header = %T, want object", card["header"])
	}
	title, ok := header["title"].(map[string]any)
	if !ok {
		t.Fatalf("title = %T, want object", header["title"])
	}
	content, _ := title["content"].(string)
	if !strings.Contains(content, "Prometheus告警") {
		t.Fatalf("title = %q, want keyword Prometheus告警", content)
	}
}

func TestLarkLegacyStatusCodeSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, `{"StatusCode":0,"StatusMessage":"success"}`)
	}))
	t.Cleanup(server.Close)

	client := newLarkClient(server.URL, defaultHTTPClient())
	if err := client.send(context.Background(), buildCardMessage(samplePayload())); err != nil {
		t.Fatalf("send: %v", err)
	}
}

func TestLarkAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, `{"code":19024,"msg":"Key Words Not Found"}`)
	}))
	t.Cleanup(server.Close)

	client := newLarkClient(server.URL, defaultHTTPClient())
	err := client.send(context.Background(), buildCardMessage(samplePayload()))
	if err == nil {
		t.Fatal("send = nil, want error")
	}
	var larkErr *larkError
	if !errors.As(err, &larkErr) {
		t.Fatalf("error = %T, want *larkError", err)
	}
	if larkErr.kind != "api" {
		t.Fatalf("kind = %q, want api", larkErr.kind)
	}
	if larkErr.code != 19024 {
		t.Fatalf("code = %d, want 19024", larkErr.code)
	}
	if larkErr.msg != "Key Words Not Found" {
		t.Fatalf("msg = %q", larkErr.msg)
	}
}

func TestLarkLegacyStatusCodeError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, `{"StatusCode":1,"StatusMessage":"failed"}`)
	}))
	t.Cleanup(server.Close)

	client := newLarkClient(server.URL, defaultHTTPClient())
	err := client.send(context.Background(), buildCardMessage(samplePayload()))
	var larkErr *larkError
	if !errors.As(err, &larkErr) {
		t.Fatalf("error = %T, want *larkError", err)
	}
	if larkErr.kind != "api" || larkErr.code != 1 {
		t.Fatalf("error = %+v, want api code 1", larkErr)
	}
}

func TestLarkNonJSONResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, `<html>gateway error</html>`)
	}))
	t.Cleanup(server.Close)

	client := newLarkClient(server.URL, defaultHTTPClient())
	err := client.send(context.Background(), buildCardMessage(samplePayload()))
	var larkErr *larkError
	if !errors.As(err, &larkErr) {
		t.Fatalf("error = %T, want *larkError", err)
	}
	if larkErr.kind != "non_json" {
		t.Fatalf("kind = %q, want non_json", larkErr.kind)
	}
}

func TestLarkHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"code":0,"msg":"internal"}`)
	}))
	t.Cleanup(server.Close)

	client := newLarkClient(server.URL, defaultHTTPClient())
	err := client.send(context.Background(), buildCardMessage(samplePayload()))
	var larkErr *larkError
	if !errors.As(err, &larkErr) {
		t.Fatalf("error = %T, want *larkError", err)
	}
	if larkErr.kind != "http_status" || larkErr.httpStatus != http.StatusInternalServerError {
		t.Fatalf("error = %+v, want http_status 500", larkErr)
	}
}

func TestLarkDoesNotFollowRedirects(t *testing.T) {
	var followed atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		followed.Add(1)
		_, _ = io.WriteString(w, `{"code":0}`)
	}))
	t.Cleanup(target.Close)

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	t.Cleanup(redirector.Close)

	client := newLarkClient(redirector.URL, defaultHTTPClient())
	err := client.send(context.Background(), buildCardMessage(samplePayload()))
	if err == nil {
		t.Fatal("send = nil, want error")
	}
	if followed.Load() != 0 {
		t.Fatal("redirect target must not be called")
	}
}

func TestLarkTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = io.WriteString(w, `{"code":0}`)
	}))
	t.Cleanup(server.Close)

	client := newLarkClient(server.URL, &http.Client{Timeout: 50 * time.Millisecond})
	err := client.send(context.Background(), buildCardMessage(samplePayload()))
	var larkErr *larkError
	if !errors.As(err, &larkErr) {
		t.Fatalf("error = %T, want *larkError", err)
	}
	if larkErr.kind != "timeout" {
		t.Fatalf("kind = %q, want timeout", larkErr.kind)
	}
}
