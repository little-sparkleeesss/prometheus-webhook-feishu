package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"time"
)

const (
	larkRequestTimeout    = 10 * time.Second
	maxLarkResponseBytes  = 64 * 1024
	maxLarkMessageLogSize = 200
)

type larkClient struct {
	url    string
	client *http.Client
}

func newLarkClient(url string, client *http.Client) *larkClient {
	return &larkClient{url: url, client: client}
}

func defaultHTTPClient() *http.Client {
	return &http.Client{
		Timeout: larkRequestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// larkResponse covers both the current "code" response and the legacy
// "StatusCode" response. Pointers keep absent fields distinguishable from 0.
type larkResponse struct {
	Code          *int   `json:"code"`
	Msg           string `json:"msg"`
	StatusCode    *int   `json:"StatusCode"`
	StatusMessage string `json:"StatusMessage"`
}

func (r larkResponse) message() string {
	if r.Msg != "" {
		return r.Msg
	}
	return r.StatusMessage
}

func (r larkResponse) primaryCode() int {
	if r.Code != nil {
		return *r.Code
	}
	if r.StatusCode != nil {
		return *r.StatusCode
	}
	return 0
}

type larkError struct {
	kind       string
	httpStatus int
	code       int
	msg        string
}

func (e *larkError) Error() string {
	return "lark request failed: " + e.kind
}

func (c *larkClient) send(ctx context.Context, msg *cardMessage) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return &larkError{kind: "encode"}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return &larkError{kind: "request"}
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := c.client.Do(req)
	if err != nil {
		return &larkError{kind: classifyNetworkError(ctx, err)}
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxLarkResponseBytes))
	if err != nil {
		return &larkError{kind: "read_response", httpStatus: resp.StatusCode}
	}

	var parsed larkResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return &larkError{kind: "non_json", httpStatus: resp.StatusCode}
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return &larkError{
			kind:       "http_status",
			httpStatus: resp.StatusCode,
			code:       parsed.primaryCode(),
			msg:        parsed.message(),
		}
	}
	if parsed.Code != nil && *parsed.Code != 0 {
		return &larkError{
			kind:       "api",
			httpStatus: resp.StatusCode,
			code:       *parsed.Code,
			msg:        parsed.message(),
		}
	}
	if parsed.StatusCode != nil && *parsed.StatusCode != 0 {
		return &larkError{
			kind:       "api",
			httpStatus: resp.StatusCode,
			code:       *parsed.StatusCode,
			msg:        parsed.message(),
		}
	}
	return nil
}

func classifyNetworkError(ctx context.Context, err error) string {
	if ctx.Err() != nil {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	return "network"
}
