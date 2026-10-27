package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Slack Block Kit limits we can hit with long test titles or errors.
const (
	slackHeaderMax  = 150
	slackSectionMax = 3000
)

// slackPayload is an Incoming Webhook message. Text is the fallback shown in
// notifications and by clients that can't render blocks.
type slackPayload struct {
	Text   string       `json:"text"`
	Blocks []slackBlock `json:"blocks"`
}

// slackBlock covers the block types we use: header, section, context and
// actions. Elements holds slackText (context) or slackButton (actions).
type slackBlock struct {
	Type     string     `json:"type"`
	Text     *slackText `json:"text,omitempty"`
	Elements []any      `json:"elements,omitempty"`
}

type slackText struct {
	Type  string `json:"type"` // plain_text or mrkdwn
	Text  string `json:"text"`
	Emoji bool   `json:"emoji,omitempty"` // plain_text only
}

type slackButton struct {
	Type  string    `json:"type"` // always "button"
	Text  slackText `json:"text"`
	URL   string    `json:"url"`
	Style string    `json:"style,omitempty"`
}

// buildSlackPayload renders a report: header, context line, run error or
// up to 10 failures, "and N more", and a "View run" button.
func buildSlackPayload(r Report) slackPayload {
	blocks := []slackBlock{
		{Type: "header", Text: &slackText{Type: "plain_text", Text: truncate(r.Emoji()+" "+r.Headline(), slackHeaderMax), Emoji: true}},
		mrkdwnContext(r.Details()),
	}
	if r.RunError != "" {
		blocks = append(blocks, mrkdwnSection("*Run error*\n"+codeSpan(r.RunError)))
	}
	for _, f := range r.Failures {
		text := "*" + slackEscape(f.Title) + "*"
		if f.ErrorLine != "" {
			text += "\n" + codeSpan(f.ErrorLine)
		}
		// STEP 9 (triage): append the category and summary line here,
		// e.g. "\n:mag: real_bug (0.92): Cart badge shows 0 after adding".
		blocks = append(blocks, mrkdwnSection(text))
	}
	if more := r.MoreText(); more != "" {
		blocks = append(blocks, mrkdwnContext(more))
	}
	blocks = append(blocks, slackBlock{
		Type: "actions",
		Elements: []any{slackButton{
			Type:  "button",
			Text:  slackText{Type: "plain_text", Text: "View run"},
			URL:   r.RunURL,
			Style: "primary",
		}},
	})
	return slackPayload{Text: r.Subject(), Blocks: blocks}
}

func mrkdwnSection(text string) slackBlock {
	return slackBlock{Type: "section", Text: &slackText{Type: "mrkdwn", Text: truncate(text, slackSectionMax)}}
}

func mrkdwnContext(text string) slackBlock {
	return slackBlock{Type: "context", Elements: []any{slackText{Type: "mrkdwn", Text: truncate(text, slackSectionMax)}}}
}

// slackEscape escapes the three characters mrkdwn treats as control
// characters, so test titles and errors can't form links or mentions.
func slackEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// codeSpan shows s as inline code. Backticks inside would end the span early,
// so they become single quotes.
func codeSpan(s string) string {
	return "`" + strings.ReplaceAll(slackEscape(s), "`", "'") + "`"
}

// slackSender posts reports to an Incoming Webhook.
type slackSender struct {
	webhookURL string // a secret: never logged or returned in errors
	client     *http.Client
}

func (s slackSender) send(ctx context.Context, r Report) error {
	body, err := json.Marshal(buildSlackPayload(r))
	if err != nil {
		return fmt.Errorf("slack: encode payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.webhookURL, bytes.NewReader(body))
	if err != nil {
		return errors.New("slack: invalid SLACK_WEBHOOK_URL")
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		// *url.Error includes the URL, and the webhook URL is the secret.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return fmt.Errorf("slack: post webhook: %w", err)
	}
	defer resp.Body.Close()
	// Slack answers "ok" or a short reason such as "invalid_payload".
	reply, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("slack: webhook returned %d: %s", resp.StatusCode, strings.TrimSpace(string(reply)))
	}
	return nil
}
