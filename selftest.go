package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// selfTestTimeout leaves room for the local server to finish its own Lark
	// call before the client gives up.
	selfTestTimeout          = larkRequestTimeout + 5*time.Second
	maxSelfTestResponseBytes = 4 << 10
)

// defaultSelfTestClient returns the HTTP client used by --self-test.
func defaultSelfTestClient() *http.Client {
	return &http.Client{
		Timeout: selfTestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// selfTestPayload builds the fixed Alertmanager payload used by --self-test.
// The alertname and cluster make the resulting card easy to identify.
func selfTestPayload() alertmanagerPayload {
	return alertmanagerPayload{
		Status: "firing",
		Alerts: []alert{{
			Status: "firing",
			Labels: map[string]string{
				"alertname": "LinkTest",
				"severity":  "info",
				"instance":  "self-test",
				"cluster":   "self-test",
			},
			Annotations: map[string]string{
				"summary":     "链路自检",
				"description": "由 --self-test 触发的链路自检, 请忽略",
			},
			StartsAt: time.Now().UTC().Format(time.RFC3339),
		}},
		CommonLabels: map[string]string{"cluster": "self-test"},
	}
}

// runSelfTest posts one alert to the local webhook and reports the outcome.
// It returns a process exit code. The token is only used in the request
// header and is never written to stdout.
func runSelfTest(getenv func(string) string, stdout io.Writer, client *http.Client) int {
	cfg, err := loadConfig(getenv)
	if err != nil {
		fmt.Fprintf(stdout, "self-test failed: invalid configuration: %s\n", err)
		return 1
	}

	body, err := json.Marshal(selfTestPayload())
	if err != nil {
		fmt.Fprintf(stdout, "self-test failed: encode payload: %s\n", err)
		return 1
	}

	endpoint := "http://127.0.0.1:" + cfg.port + webhookPath
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(stdout, "self-test failed: build request: %s\n", err)
		return 1
	}
	req.Header.Set("Authorization", authorizationScheme+cfg.token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(stdout, "self-test failed: local webhook request: %s\n", err)
		return 1
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxSelfTestResponseBytes))
	message := strings.TrimSpace(string(respBody))
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(stdout, "self-test failed: HTTP %d: %s (check container logs for details)\n", resp.StatusCode, message)
		return 1
	}

	fmt.Fprintf(stdout, "self-test passed: HTTP %d: %s\n", resp.StatusCode, message)
	return 0
}
