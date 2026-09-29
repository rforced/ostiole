package model

import (
	"errors"
	"strings"
	"testing"
)

// notifyConfig is a starter sending by mail and to a webhook.
func notifyConfig(t *testing.T) *Config {
	t.Helper()
	cfg := Starter(StarterOptions{Hostname: "router", LAN: "eth0", LANAddress: "192.168.1.1/24"})
	cfg.Notifications = Notifications{
		Enabled: true,
		Email: NotifyEmail{
			Enabled: true, Server: "smtp.example.net", Username: "router", Password: "hunter2",
			From: "Router <router@example.net>", To: []string{"admin@example.net"},
		},
		Webhook: NotifyWebhook{Enabled: true, URL: "https://ntfy.example.net/router", Format: WebhookNtfy, Token: "tk_secret"},
		Mute:    []string{"waf"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the base configuration is invalid: %v", err)
	}
	return cfg
}

func TestValidateNotifications(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		edit func(n *Notifications)
		path string
	}{
		{"on with nowhere to send", func(n *Notifications) { n.Email.Enabled, n.Webhook.Enabled = false, false }, "notifications.enabled"},
		{"no mail server", func(n *Notifications) { n.Email.Server = "" }, "notifications.email.server"},
		{"a URL for a server", func(n *Notifications) { n.Email.Server = "smtp://mail.example.net" }, "notifications.email.server"},
		{"port zero", func(n *Notifications) { n.Email.Server = "mail.example.net:0" }, "notifications.email.server"},
		{"an unknown security", func(n *Notifications) { n.Email.Security = "ssl" }, "notifications.email.security"},
		{"a password in the clear", func(n *Notifications) { n.Email.Security = SMTPNone }, "notifications.email.security"},
		{"a header in the sender", func(n *Notifications) { n.Email.From = "a@example.net\r\nBcc: b@example.net" }, "notifications.email.from"},
		{"no sender", func(n *Notifications) { n.Email.From = "" }, "notifications.email.from"},
		{"no recipient", func(n *Notifications) { n.Email.To = nil }, "notifications.email.to"},
		{"a recipient that is not one", func(n *Notifications) { n.Email.To = []string{"admin"} }, "notifications.email.to[0]"},
		{"plain http", func(n *Notifications) { n.Webhook.URL = "http://ntfy.example.net/router" }, "notifications.webhook.url"},
		{"credentials in the URL", func(n *Notifications) { n.Webhook.URL = "https://u:p@ntfy.example.net/x" }, "notifications.webhook.url"},
		{"no URL", func(n *Notifications) { n.Webhook.URL = "" }, "notifications.webhook.url"},
		{"an unknown format", func(n *Notifications) { n.Webhook.Format = "teams" }, "notifications.webhook.format"},
		{"a token with a space", func(n *Notifications) { n.Webhook.Token = "Bearer x" }, "notifications.webhook.token"},
		{"a mute that is no kind", func(n *Notifications) { n.Mute = []string{"Gateway Down"} }, "notifications.mute[0]"},
	} {
		cfg := notifyConfig(t)
		tc.edit(&cfg.Notifications)
		err := cfg.Validate()
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("%s: error %v, want a validation error on %s", tc.name, err, tc.path)
			continue
		}
		if !hasPath(ve, tc.path) {
			t.Errorf("%s: issues %v, want one on %s", tc.name, ve.Issues, tc.path)
		}
	}
}

// A chat service's webhook URL is the key to its channel, so a refusal
// does not repeat it where a log or a screenshot would keep it.
func TestValidateDoesNotRepeatTheWebhookURL(t *testing.T) {
	t.Parallel()
	cfg := notifyConfig(t)
	cfg.Notifications.Webhook.URL = "http://hooks.example.net/services/T000/B000/XXXXSECRET"
	err := cfg.Validate()
	if err == nil || strings.Contains(err.Error(), "XXXXSECRET") {
		t.Errorf("error = %v", err)
	}
}

// A target filled in but switched off is no one's problem yet, and a mute
// this build does not know is left for the one that does.
func TestValidateAcceptsUnfinishedNotifications(t *testing.T) {
	t.Parallel()
	cfg := Starter(StarterOptions{Hostname: "router", LAN: "eth0", LANAddress: "192.168.1.1/24"})
	cfg.Notifications = Notifications{Email: NotifyEmail{Server: "smtp.example.net"}, Mute: []string{"from-the-future"}}
	if err := cfg.Validate(); err != nil {
		t.Errorf("a switched-off draft was refused: %v", err)
	}
}

func TestNotifyEmailAddressFollowsTheSecurity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ server, security, want string }{
		{"smtp.example.net", "", "smtp.example.net:587"},
		{"smtp.example.net", SMTPTLS, "smtp.example.net:465"},
		{"relay.lan", SMTPNone, "relay.lan:25"},
		{"smtp.example.net:2525", SMTPTLS, "smtp.example.net:2525"},
		{"2001:db8::25", SMTPNone, "[2001:db8::25]:25"},
		{"[2001:db8::25]:26", "", "[2001:db8::25]:26"},
	} {
		if got := (NotifyEmail{Server: tc.server, Security: tc.security}).Address(); got != tc.want {
			t.Errorf("Address(%q, %q) = %q, want %q", tc.server, tc.security, got, tc.want)
		}
	}
}

func TestRedactedBlanksTheNotificationSecrets(t *testing.T) {
	t.Parallel()
	n := notifyConfig(t).Redacted().Notifications
	if n.Email.Password != "" || n.Webhook.Token != "" || n.Webhook.URL != "" {
		t.Errorf("redacted notifications = %+v, want the password, token and URL blank", n)
	}
	if n.Email.Server != "smtp.example.net" || !n.Enabled {
		t.Errorf("redacted notifications = %+v, want the rest kept", n)
	}
}

func TestNotificationsAreAnAdminChange(t *testing.T) {
	t.Parallel()
	old := notifyConfig(t)
	next := notifyConfig(t)
	next.Notifications.Webhook.URL = "https://elsewhere.example.net/"
	if got := AdminChanges(old, next); len(got) != 1 || got[0] != "notifications" {
		t.Errorf("AdminChanges = %q", got)
	}
}
