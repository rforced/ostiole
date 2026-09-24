package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/rforced/ostiole/internal/model"
)

// hook is a receiver that records what it was sent and answers as told.
type hook struct {
	mu     sync.Mutex
	got    []*http.Request
	bodies []string
	status int
}

func (h *hook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	h.mu.Lock()
	h.got = append(h.got, r)
	h.bodies = append(h.bodies, string(body))
	status := h.status
	h.mu.Unlock()
	if status == http.StatusFound {
		http.Redirect(w, r, "https://elsewhere.example.net/", status)
		return
	}
	if status != 0 {
		http.Error(w, "no such channel", status)
	}
}

// seen is what the receiver has been sent so far.
func (h *hook) seen() ([]*http.Request, []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]*http.Request(nil), h.got...), append([]string(nil), h.bodies...)
}

func receiver(t *testing.T, status int) (*hook, *httptest.Server, Delivery) {
	t.Helper()
	h := &hook{status: status}
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	client := srv.Client()
	client.CheckRedirect = noRedirects.CheckRedirect
	return h, srv, Delivery{Client: client}
}

var down = Message{Router: "fw", Events: []Event{{Kind: "gateway-down", Level: LevelWarn, Title: "Gateway wan is down", Time: at}}}

func TestWebhookFormats(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		format string
		check  func(t *testing.T, r *http.Request, body string)
	}{
		{model.WebhookJSON, func(t *testing.T, _ *http.Request, body string) {
			var p payload
			if err := json.Unmarshal([]byte(body), &p); err != nil || p.Router != "fw" || len(p.Events) != 1 || p.Events[0].Kind != "gateway-down" {
				t.Errorf("body = %s (%v)", body, err)
			}
		}},
		{model.WebhookSlack, func(t *testing.T, _ *http.Request, body string) {
			var p map[string]string
			if err := json.Unmarshal([]byte(body), &p); err != nil || !strings.HasPrefix(p["text"], "fw: Gateway wan is down\n\n") {
				t.Errorf("body = %s (%v)", body, err)
			}
		}},
		{model.WebhookDiscord, func(t *testing.T, _ *http.Request, body string) {
			var p struct {
				Content  string `json:"content"`
				Mentions *struct {
					Parse []string `json:"parse"`
				} `json:"allowed_mentions"`
			}
			if err := json.Unmarshal([]byte(body), &p); err != nil || !strings.HasPrefix(p.Content, "**fw: Gateway wan is down**\n") ||
				p.Mentions == nil || p.Mentions.Parse == nil || len(p.Mentions.Parse) != 0 {
				t.Errorf("body = %s (%v)", body, err)
			}
		}},
		{model.WebhookNtfy, func(t *testing.T, r *http.Request, body string) {
			if r.Header.Get("Title") != "fw: Gateway wan is down" || r.Header.Get("Priority") != "high" ||
				!strings.HasPrefix(r.Header.Get("Content-Type"), "text/plain") || !strings.HasPrefix(body, "Gateway wan is down\n") {
				t.Errorf("headers %v, body %q", r.Header, body)
			}
		}},
	} {
		t.Run(tc.format, func(t *testing.T) {
			t.Parallel()
			h, srv, d := receiver(t, 0)
			w := model.NotifyWebhook{Enabled: true, URL: srv.URL + "/hook", Format: tc.format, Token: "tk_1"}
			if err := d.Send(context.Background(), TargetWebhook, model.Notifications{Webhook: w}, down); err != nil {
				t.Fatal(err)
			}
			got, bodies := h.seen()
			r := got[0]
			if r.Method != http.MethodPost || r.URL.Path != "/hook" || r.Header.Get("Authorization") != "Bearer tk_1" || r.Header.Get("User-Agent") != "ostiole" {
				t.Errorf("request = %s %s %v", r.Method, r.URL, r.Header)
			}
			tc.check(t, r, bodies[0])
		})
	}
}

// A refusal or a redirect is no delivery, and neither error repeats the
// path, which is where a chat service keeps the key to the channel.
func TestAWebhookThatDoesNotTakeItFails(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusNotFound, http.StatusFound} {
		h, srv, d := receiver(t, status)
		w := model.NotifyWebhook{Enabled: true, URL: srv.URL + "/services/T0/B0/SECRETKEY"}
		err := d.Send(context.Background(), TargetWebhook, model.Notifications{Webhook: w}, down)
		if err == nil || strings.Contains(err.Error(), "SECRETKEY") || !strings.Contains(err.Error(), "answered") {
			t.Errorf("status %d: err = %v", status, err)
		}
		if got, _ := h.seen(); len(got) != 1 {
			t.Errorf("status %d: the receiver saw %d requests", status, len(got))
		}
	}
	w := model.NotifyWebhook{Enabled: true, URL: "https://127.0.0.1:1/services/SECRETKEY"}
	err := (Delivery{}).Send(context.Background(), TargetWebhook, model.Notifications{Webhook: w}, down)
	if err == nil || strings.Contains(err.Error(), "SECRETKEY") {
		t.Errorf("unreachable: err = %v", err)
	}
}

// Text passed on from a publisher or a CA does not ping a Slack channel.
func TestSlackTextCannotMention(t *testing.T) {
	t.Parallel()
	h, srv, d := receiver(t, 0)
	m := Message{Router: "fw", Events: []Event{{Title: "Blocklist x is not refreshing", Detail: "503 <!channel> & <users/all>", Time: at}}}
	w := model.NotifyWebhook{Enabled: true, URL: srv.URL, Format: model.WebhookSlack}
	if err := d.Send(context.Background(), TargetWebhook, model.Notifications{Webhook: w}, m); err != nil {
		t.Fatal(err)
	}
	_, bodies := h.seen()
	var p map[string]string
	if err := json.Unmarshal([]byte(bodies[0]), &p); err != nil || !strings.Contains(p["text"], "503 &lt;!channel&gt; &amp; &lt;users/all&gt;") {
		t.Errorf("body = %s (%v)", bodies[0], err)
	}
}
