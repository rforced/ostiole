package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rforced/ostiole/internal/model"
)

// discordLimit is the most a Discord message may hold, in characters;
// clipping bytes short of it keeps well inside.
const discordLimit = 2000

// payload is the JSON format's body.
type payload struct {
	Router  string  `json:"router"`
	Events  []Event `json:"events"`
	Dropped int     `json:"dropped,omitempty"`
}

// slackEscape is the escaping Slack's text asks for, which also keeps
// Google Chat's <users/all> from being one.
var slackEscape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

var noRedirects = &http.Client{
	Timeout: 30 * time.Second,
	// A redirect is not delivery: a POST followed becomes a GET, and the
	// channel's key would go wherever it points.
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// post sends m to a webhook in the format it asks for.
func (d Delivery) post(ctx context.Context, w model.NotifyWebhook, m Message) error {
	var body []byte
	var err error
	contentType := "application/json"
	switch w.FormatOr() {
	case model.WebhookSlack:
		// What a notice passes on from a daemon or a publisher must not
		// ping the channel: <!channel> and its kind are escaped.
		body, err = json.Marshal(map[string]string{"text": slackEscape.Replace(m.Subject() + "\n\n" + m.Text())})
	case model.WebhookDiscord:
		body, err = json.Marshal(map[string]any{
			"content":          clip("**"+m.Subject()+"**\n"+m.Text(), discordLimit-10),
			"allowed_mentions": map[string][]string{"parse": {}},
		})
	case model.WebhookNtfy:
		body, contentType = []byte(m.Text()), "text/plain; charset=utf-8"
	default:
		events := m.Events
		if events == nil {
			events = []Event{}
		}
		body, err = json.Marshal(payload{Router: m.Router, Events: events, Dropped: m.Dropped})
	}
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSpace(w.URL), bytes.NewReader(body))
	if err != nil {
		// The URL holds a chat service's key; the error would repeat it.
		return errors.New("the webhook URL does not parse")
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", "ostiole")
	if w.Token != "" {
		req.Header.Set("Authorization", "Bearer "+w.Token)
	}
	if w.FormatOr() == model.WebhookNtfy {
		req.Header.Set("Title", mime.QEncoding.Encode("utf-8", m.Subject()))
		switch m.level() {
		case LevelWarn:
			req.Header.Set("Priority", "high")
			req.Header.Set("Tags", "warning")
		case LevelOK:
			req.Header.Set("Tags", "white_check_mark")
		}
	}
	client := d.Client
	if client == nil {
		client = noRedirects
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("post to %s: %w", req.URL.Host, unwrapURL(err))
	}
	defer func() { _ = resp.Body.Close() }()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := oneLine(string(answer))
		if msg != "" {
			msg = ": " + clip(msg, 200)
		}
		return fmt.Errorf("%s answered %s%s", req.URL.Host, resp.Status, msg)
	}
	return nil
}

// unwrapURL drops the *url.Error wrapper, which quotes the whole URL.
func unwrapURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}
