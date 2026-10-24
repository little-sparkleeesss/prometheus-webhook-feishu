package main

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func samplePayload() alertmanagerPayload {
	return alertmanagerPayload{
		Status: "firing",
		Alerts: []alert{
			{
				Status: "firing",
				Labels: map[string]string{
					"alertname": "HighCPU",
					"severity":  "critical",
					"instance":  "10.0.0.1:9100",
				},
				Annotations: map[string]string{
					"summary":     "CPU usage is high",
					"description": "CPU usage is above 90% for 5 minutes",
				},
				StartsAt: "2026-10-24T12:00:00Z",
			},
		},
		CommonLabels: map[string]string{"cluster": "prod"},
	}
}

func noteContents(msg *cardMessage) []string {
	var contents []string
	for _, element := range msg.Card.Elements {
		note, ok := element.(noteElement)
		if !ok {
			continue
		}
		for _, item := range note.Elements {
			contents = append(contents, item.Content)
		}
	}
	return contents
}

func hasNoteContaining(msg *cardMessage, substring string) bool {
	for _, content := range noteContents(msg) {
		if strings.Contains(content, substring) {
			return true
		}
	}
	return false
}

func TestBuildCardMessageFiring(t *testing.T) {
	msg := buildCardMessage(samplePayload())

	if msg.MsgType != "interactive" {
		t.Fatalf("msg_type = %q, want interactive", msg.MsgType)
	}
	if !msg.Card.Config.WideScreenMode || !msg.Card.Config.EnableForward {
		t.Fatalf("card config = %+v, want wide_screen_mode and enable_forward", msg.Card.Config)
	}
	if msg.Card.Header.Template != "red" {
		t.Fatalf("template = %q, want red", msg.Card.Header.Template)
	}
	if msg.Card.Header.Title.Tag != "plain_text" {
		t.Fatalf("title tag = %q, want plain_text", msg.Card.Header.Title.Tag)
	}
	if msg.Card.Header.Title.Content != "Prometheus告警触发" {
		t.Fatalf("title = %q, want Prometheus告警触发", msg.Card.Header.Title.Content)
	}

	encoded, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal card: %v", err)
	}
	if strings.Contains(string(encoded), `"schema"`) {
		t.Fatal("card must not contain schema 2.0 field")
	}
	if !strings.Contains(string(encoded), "Prometheus告警") {
		t.Fatal("card must contain the keyword Prometheus告警")
	}
}

func TestBuildCardMessageResolved(t *testing.T) {
	payload := samplePayload()
	payload.Status = "resolved"

	msg := buildCardMessage(payload)

	if msg.Card.Header.Template != "green" {
		t.Fatalf("template = %q, want green", msg.Card.Header.Template)
	}
	if msg.Card.Header.Title.Content != "Prometheus告警恢复" {
		t.Fatalf("title = %q, want Prometheus告警恢复", msg.Card.Header.Title.Content)
	}
	if !hasNoteContaining(msg, "Prometheus告警") {
		t.Fatal("note must contain the keyword Prometheus告警")
	}
}

func TestBuildCardMessageMultipleAlerts(t *testing.T) {
	payload := samplePayload()
	payload.Alerts = append(payload.Alerts, alert{
		Labels:      map[string]string{"alertname": "HighMemory", "severity": "warning"},
		Annotations: map[string]string{"summary": "memory high"},
		StartsAt:    "2026-10-24T13:00:00Z",
	})

	msg := buildCardMessage(payload)

	if len(msg.Card.Elements) != 8 {
		t.Fatalf("elements = %d, want 8", len(msg.Card.Elements))
	}
	if _, ok := msg.Card.Elements[3].(hrElement); !ok {
		t.Fatalf("element 3 = %T, want hrElement", msg.Card.Elements[3])
	}
	encoded, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal card: %v", err)
	}
	if !strings.Contains(string(encoded), `"tag":"hr"`) {
		t.Fatal("card must contain an hr separator")
	}
}

func TestBuildCardMessageMissingLabelsAndAnnotations(t *testing.T) {
	payload := alertmanagerPayload{
		Status: "firing",
		Alerts: []alert{{}},
	}

	msg := buildCardMessage(payload)

	first, ok := msg.Card.Elements[0].(divFieldsElement)
	if !ok {
		t.Fatalf("element 0 = %T, want divFieldsElement", msg.Card.Elements[0])
	}
	if first.Fields[0].Text.Content != "**告警名称**:\n-" {
		t.Fatalf("alertname content = %q", first.Fields[0].Text.Content)
	}
	if first.Fields[1].Text.Content != "**严重等级**:\n-" {
		t.Fatalf("severity content = %q", first.Fields[1].Text.Content)
	}
	second, ok := msg.Card.Elements[1].(divFieldsElement)
	if !ok {
		t.Fatalf("element 1 = %T, want divFieldsElement", msg.Card.Elements[1])
	}
	if second.Fields[0].Text.Content != "**集群**:\n-" {
		t.Fatalf("cluster content = %q", second.Fields[0].Text.Content)
	}
	if second.Fields[1].Text.Content != "**实例**:\n-" {
		t.Fatalf("instance content = %q", second.Fields[1].Text.Content)
	}
	detail, ok := msg.Card.Elements[2].(divTextElement)
	if !ok {
		t.Fatalf("element 2 = %T, want divTextElement", msg.Card.Elements[2])
	}
	if detail.Text.Content != "**详情**:\n-" {
		t.Fatalf("detail content = %q", detail.Text.Content)
	}

	encoded, _ := json.Marshal(msg)
	if strings.Contains(string(encoded), "<no value>") {
		t.Fatal("card must not contain <no value>")
	}
}

func TestRenderValueEscapesLarkMD(t *testing.T) {
	input := "a\\b*c_d~e`f[g]h"
	want := "a\\\\b\\*c\\_d\\~e\\`f\\[g\\]h"

	if got := renderValue(input, maxFieldRunes); got != want {
		t.Fatalf("renderValue = %q, want %q", got, want)
	}
}

func TestRenderValueReplacesNoValue(t *testing.T) {
	if got := renderValue("<no value>", maxFieldRunes); got != "-" {
		t.Fatalf("renderValue = %q, want -", got)
	}
}

func TestRenderValueTruncatesByRune(t *testing.T) {
	longField := strings.Repeat("告", 600)
	gotField := renderValue(longField, maxFieldRunes)
	if count := utf8.RuneCountInString(gotField); count != maxFieldRunes {
		t.Fatalf("field rune count = %d, want %d", count, maxFieldRunes)
	}

	longDetail := strings.Repeat("详", 1100)
	gotDetail := renderValue(longDetail, maxDetailRunes)
	if count := utf8.RuneCountInString(gotDetail); count != maxDetailRunes {
		t.Fatalf("detail rune count = %d, want %d", count, maxDetailRunes)
	}

	escaped := renderValue(strings.Repeat("*", 600), maxFieldRunes)
	if count := utf8.RuneCountInString(escaped); count > maxFieldRunes {
		t.Fatalf("escaped field rune count = %d, want <= %d", count, maxFieldRunes)
	}
	if strings.HasSuffix(escaped, "\\") {
		t.Fatal("escaped field must not end with a dangling backslash")
	}
}

func TestBuildCardMessageSizeLimit(t *testing.T) {
	payload := samplePayload()
	payload.Alerts = make([]alert, maxRenderedAlerts)
	for i := range payload.Alerts {
		payload.Alerts[i] = alert{
			Labels: map[string]string{
				"alertname": strings.Repeat("名", maxFieldRunes),
				"severity":  "critical",
				"instance":  strings.Repeat("i", maxFieldRunes),
				"cluster":   "prod",
			},
			Annotations: map[string]string{
				"description": strings.Repeat("详", maxDetailRunes),
			},
		}
	}
	payload.CommonLabels = nil

	msg := buildCardMessage(payload)
	encoded, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal card: %v", err)
	}
	if len(encoded) > maxCardBytes {
		t.Fatalf("card size = %d bytes, want <= %d", len(encoded), maxCardBytes)
	}
	if !hasNoteContaining(msg, "条未展示") {
		t.Fatal("oversized card must report omitted alerts")
	}
}

func TestBuildCardMessageOmittedAlerts(t *testing.T) {
	payload := samplePayload()
	payload.Alerts = make([]alert, 12)
	for i := range payload.Alerts {
		payload.Alerts[i] = alert{
			Labels:      map[string]string{"alertname": "alert", "severity": "warning"},
			Annotations: map[string]string{"description": "detail"},
		}
	}

	msg := buildCardMessage(payload)

	if !hasNoteContaining(msg, "还有 2 条未展示") {
		t.Fatalf("notes = %v, want 还有 2 条未展示", noteContents(msg))
	}
}

func TestBuildCardMessageTruncatedAlerts(t *testing.T) {
	payload := samplePayload()
	payload.TruncatedAlerts = 3

	msg := buildCardMessage(payload)

	if !hasNoteContaining(msg, "还有 3 条未展示") {
		t.Fatalf("notes = %v, want 还有 3 条未展示", noteContents(msg))
	}
}

func TestFormatStartsAt(t *testing.T) {
	cases := map[string]string{
		"2026-10-24T12:00:00Z":           "2026-10-24 20:00:00",
		"2026-10-24T12:00:00+00:00":      "2026-10-24 20:00:00",
		"2026-10-24T12:00:00.123456789Z": "2026-10-24 20:00:00",
		"not-a-time":                     "not-a-time",
		"":                               "-",
	}
	for input, want := range cases {
		if got := formatStartsAt(input); got != want {
			t.Fatalf("formatStartsAt(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestClusterValueFallback(t *testing.T) {
	payload := samplePayload()
	alertWithCluster := alert{Labels: map[string]string{"cluster": "edge"}}

	if got := clusterValue(payload, alertWithCluster); got != "prod" {
		t.Fatalf("common cluster = %q, want prod", got)
	}

	payload.CommonLabels = nil
	if got := clusterValue(payload, alertWithCluster); got != "edge" {
		t.Fatalf("alert cluster = %q, want edge", got)
	}

	if got := clusterValue(payload, alert{}); got != "-" {
		t.Fatalf("missing cluster = %q, want -", got)
	}
}
