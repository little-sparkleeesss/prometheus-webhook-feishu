package main

import (
	"bytes"
	"errors"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func buildTestBinary(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "prometheus-webhook-feishu")
	cmd := exec.Command("go", "build", "-o", binary, ".")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build binary: %v\n%s", err, output)
	}
	return binary
}

func TestMainRejectsMissingToken(t *testing.T) {
	binary := buildTestBinary(t)
	cmd := exec.Command(binary)
	cmd.Env = []string{"FEISHU_WEBHOOK_URL=" + validWebhookURL}

	var output syncBuffer
	cmd.Stdout = &output
	cmd.Stderr = &output

	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("run = %v, want a non-zero exit", err)
	}
	if exitErr.ExitCode() != 1 {
		t.Fatalf("exit code = %d, want 1", exitErr.ExitCode())
	}
	if !strings.Contains(output.String(), "startup_failed") {
		t.Fatalf("output = %q, want startup_failed", output.String())
	}
}

func TestMainServesAndShutsDown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM is not supported on windows")
	}
	binary := buildTestBinary(t)
	port := freePort(t)

	cmd := exec.Command(binary)
	cmd.Env = []string{
		"FEISHU_WEBHOOK_URL=" + validWebhookURL,
		"WEBHOOK_TOKEN=" + testToken,
		"PORT=" + port,
	}
	var output syncBuffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})

	client := &http.Client{Timeout: 500 * time.Millisecond}
	url := "http://127.0.0.1:" + port + "/healthz"
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not become healthy; output: %s", output.String())
		}
		time.Sleep(25 * time.Millisecond)
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("wait = %v; output: %s", err, output.String())
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("server did not exit after SIGTERM; output: %s", output.String())
	}
	if !strings.Contains(output.String(), "shutdown_complete") {
		t.Fatalf("output = %q, want shutdown_complete", output.String())
	}
}

func freePort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	defer listener.Close()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("split port: %v", err)
	}
	return port
}
