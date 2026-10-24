package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxRenderedAlerts = 10
	maxFieldRunes     = 512
	maxDetailRunes    = 1000
	maxCardBytes      = 18 * 1024
	utc8OffsetSeconds = 8 * 60 * 60
	timeLayout        = "2006-01-02 15:04:05"
)

var utc8 = time.FixedZone("UTC+8", utc8OffsetSeconds)

// alertmanagerPayload is the subset of the Alertmanager v4 webhook payload
// that the notifier consumes.
type alertmanagerPayload struct {
	Status            string            `json:"status"`
	Alerts            []alert           `json:"alerts"`
	TruncatedAlerts   int               `json:"truncatedAlerts"`
	CommonLabels      map[string]string `json:"commonLabels"`
	CommonAnnotations map[string]string `json:"commonAnnotations"`
}

type alert struct {
	Status       string            `json:"status"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     string            `json:"startsAt"`
	EndsAt       string            `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
	Fingerprint  string            `json:"fingerprint"`
}

func (p *alertmanagerPayload) validate() error {
	if p.Status == "" {
		return errors.New("status is required")
	}
	if p.Alerts == nil {
		return errors.New("alerts is required")
	}
	return nil
}

// Lark interactive card v1 structures.
type cardMessage struct {
	MsgType string `json:"msg_type"`
	Card    card   `json:"card"`
}

type card struct {
	Config   cardConfig `json:"config"`
	Header   cardHeader `json:"header"`
	Elements []any      `json:"elements"`
}

type cardConfig struct {
	WideScreenMode bool `json:"wide_screen_mode"`
	EnableForward  bool `json:"enable_forward"`
}

type cardHeader struct {
	Template string `json:"template"`
	Title    text   `json:"title"`
}

type text struct {
	Tag     string `json:"tag"`
	Content string `json:"content"`
}

type divFieldsElement struct {
	Tag    string  `json:"tag"`
	Fields []field `json:"fields"`
}

type field struct {
	IsShort bool `json:"is_short"`
	Text    text `json:"text"`
}

type divTextElement struct {
	Tag  string `json:"tag"`
	Text text   `json:"text"`
}

type noteElement struct {
	Tag      string `json:"tag"`
	Elements []text `json:"elements"`
}

type hrElement struct {
	Tag string `json:"tag"`
}

// buildCardMessage renders at most maxRenderedAlerts alerts and shrinks the
// rendered set if needed so the encoded message stays within maxCardBytes.
func buildCardMessage(p alertmanagerPayload) *cardMessage {
	renderable := len(p.Alerts)
	if renderable > maxRenderedAlerts {
		renderable = maxRenderedAlerts
	}
	for shown := renderable; shown >= 0; shown-- {
		msg := renderCardMessage(p, shown)
		encoded, err := json.Marshal(msg)
		if err == nil && len(encoded) <= maxCardBytes {
			return msg
		}
	}
	return renderCardMessage(p, 0)
}

func renderCardMessage(p alertmanagerPayload, shown int) *cardMessage {
	resolved := p.Status == "resolved"
	title := "Prometheus告警触发"
	template := "red"
	if resolved {
		title = "Prometheus告警恢复"
		template = "green"
	}

	elements := make([]any, 0, shown*3+2)
	for i := 0; i < shown && i < len(p.Alerts); i++ {
		if i > 0 {
			elements = append(elements, hrElement{Tag: "hr"})
		}
		a := p.Alerts[i]
		elements = append(elements,
			divFieldsElement{
				Tag: "div",
				Fields: []field{
					newField("告警名称", renderValue(a.Labels["alertname"], maxFieldRunes)),
					newField("严重等级", renderValue(a.Labels["severity"], maxFieldRunes)),
				},
			},
			divFieldsElement{
				Tag: "div",
				Fields: []field{
					newField("集群", clusterValue(p, a)),
					newField("实例", renderValue(a.Labels["instance"], maxFieldRunes)),
				},
			},
			divTextElement{
				Tag:  "div",
				Text: text{Tag: "lark_md", Content: "**详情**:\n" + detailValue(a.Annotations)},
			},
		)
	}

	elements = append(elements, noteElement{
		Tag:      "note",
		Elements: []text{{Tag: "plain_text", Content: "Prometheus告警 | 开始时间: " + startsAtValue(p)}},
	})

	if omitted := omittedCount(p, shown); omitted > 0 {
		elements = append(elements, noteElement{
			Tag:      "note",
			Elements: []text{{Tag: "plain_text", Content: fmt.Sprintf("还有 %d 条未展示", omitted)}},
		})
	}

	return &cardMessage{
		MsgType: "interactive",
		Card: card{
			Config:   cardConfig{WideScreenMode: true, EnableForward: true},
			Header:   cardHeader{Template: template, Title: text{Tag: "plain_text", Content: title}},
			Elements: elements,
		},
	}
}

func newField(label, value string) field {
	return field{
		IsShort: true,
		Text:    text{Tag: "lark_md", Content: "**" + label + "**:\n" + value},
	}
}

func omittedCount(p alertmanagerPayload, shown int) int {
	omitted := len(p.Alerts) - shown
	if p.TruncatedAlerts > 0 {
		omitted += p.TruncatedAlerts
	}
	if omitted < 0 {
		return 0
	}
	return omitted
}

func clusterValue(p alertmanagerPayload, a alert) string {
	if v := cleanValue(p.CommonLabels["cluster"]); v != "" {
		return renderValue(v, maxFieldRunes)
	}
	return renderValue(a.Labels["cluster"], maxFieldRunes)
}

func detailValue(annotations map[string]string) string {
	if v := cleanValue(annotations["description"]); v != "" {
		return renderValue(v, maxDetailRunes)
	}
	if v := cleanValue(annotations["summary"]); v != "" {
		return renderValue(v, maxDetailRunes)
	}
	return "-"
}

func cleanValue(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "<no value>" {
		return ""
	}
	return trimmed
}

// renderValue escapes lark_md metacharacters and enforces a rune limit on the
// rendered text.
func renderValue(raw string, limit int) string {
	value := cleanValue(raw)
	if value == "" {
		return "-"
	}
	escaped := escapeLarkMD(truncateRunes(value, limit))
	return truncateRunes(escaped, limit)
}

func startsAtValue(p alertmanagerPayload) string {
	if len(p.Alerts) == 0 {
		return "-"
	}
	return formatStartsAt(p.Alerts[0].StartsAt)
}

func formatStartsAt(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" || value == "<no value>" {
		return "-"
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return truncateRunes(value, maxFieldRunes)
	}
	return parsed.In(utc8).Format(timeLayout)
}

var larkMDEscaper = strings.NewReplacer(
	`\`, `\\`,
	`*`, `\*`,
	`_`, `\_`,
	`~`, `\~`,
	"`", "\\`",
	`[`, `\[`,
	`]`, `\]`,
)

func escapeLarkMD(value string) string {
	return larkMDEscaper.Replace(value)
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)[:limit]
	// Do not leave a dangling escape character after truncation.
	if runes[len(runes)-1] == '\\' {
		runes = runes[:len(runes)-1]
	}
	return string(runes)
}
