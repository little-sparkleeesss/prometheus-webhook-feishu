package main

import (
	"testing"
)

const validWebhookURL = "https://open.feishu.cn/open-apis/bot/v2/hook/abc"

func TestValidateWebhookURLValid(t *testing.T) {
	urls := []string{
		"https://open.feishu.cn/open-apis/bot/v2/hook/abc",
		"https://open.larksuite.com/open-apis/bot/v2/hook/xyz",
		"https://OPEN.FEISHU.CN/open-apis/bot/v2/hook/abc",
	}
	for _, raw := range urls {
		if err := validateWebhookURL(raw); err != nil {
			t.Fatalf("validateWebhookURL(%q) = %v, want nil", raw, err)
		}
	}
}

func TestValidateWebhookURLInvalid(t *testing.T) {
	urls := []string{
		"",
		"not a url",
		"http://open.feishu.cn/open-apis/bot/v2/hook/abc",
		"https://evil.example.com/open-apis/bot/v2/hook/abc",
		"https://open.feishu.cn.evil.example.com/open-apis/bot/v2/hook/abc",
		"https://open.feishu.cn:8443/open-apis/bot/v2/hook/abc",
		"https://user:pass@open.feishu.cn/open-apis/bot/v2/hook/abc",
		"https://open.feishu.cn/webhook/abc",
		"https://open.feishu.cn/open-apis/bot/v2/hook/",
		"https://open.feishu.cn/open-apis/bot/v2/hook",
	}
	for _, raw := range urls {
		if err := validateWebhookURL(raw); err == nil {
			t.Fatalf("validateWebhookURL(%q) = nil, want error", raw)
		}
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	env := map[string]string{
		"FEISHU_WEBHOOK_URL": validWebhookURL,
		"WEBHOOK_TOKEN":      "secret",
	}
	cfg, err := loadConfig(func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.port != "5000" {
		t.Fatalf("port = %q, want 5000", cfg.port)
	}
	if cfg.token != "secret" {
		t.Fatalf("token = %q, want secret", cfg.token)
	}
}

func TestLoadConfigRejectsPlaceholderToken(t *testing.T) {
	for _, token := range []string{"", "<no value>"} {
		env := map[string]string{
			"FEISHU_WEBHOOK_URL": validWebhookURL,
			"WEBHOOK_TOKEN":      token,
		}
		if _, err := loadConfig(func(key string) string { return env[key] }); err == nil {
			t.Fatalf("loadConfig with WEBHOOK_TOKEN=%q = nil error, want error", token)
		}
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	env := map[string]string{
		"FEISHU_WEBHOOK_URL": validWebhookURL,
		"WEBHOOK_TOKEN":      "secret",
		"PORT":               "8080",
	}
	cfg, err := loadConfig(func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.port != "8080" {
		t.Fatalf("port = %q, want 8080", cfg.port)
	}
	if cfg.token != "secret" {
		t.Fatalf("token = %q, want secret", cfg.token)
	}
}

func TestLoadConfigErrors(t *testing.T) {
	cases := []map[string]string{
		{},
		{"FEISHU_WEBHOOK_URL": validWebhookURL, "WEBHOOK_TOKEN": "secret", "PORT": "abc"},
		{"FEISHU_WEBHOOK_URL": validWebhookURL, "WEBHOOK_TOKEN": "secret", "PORT": "0"},
		{"FEISHU_WEBHOOK_URL": validWebhookURL, "WEBHOOK_TOKEN": "secret", "PORT": "70000"},
	}
	for _, env := range cases {
		if _, err := loadConfig(func(key string) string { return env[key] }); err == nil {
			t.Fatalf("loadConfig(%v) = nil error, want error", env)
		}
	}
}
