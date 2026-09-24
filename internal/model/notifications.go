package model

import (
	"net"
	"net/mail"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// Notifications sends what the dashboard warns about, and a few things it
// does not, off the router by email or to a webhook. Nothing leaves the
// router until an admin turns it on.
type Notifications struct {
	Enabled bool          `json:"enabled,omitempty"`
	Email   NotifyEmail   `json:"email,omitzero"`
	Webhook NotifyWebhook `json:"webhook,omitzero"`
	// Mute lists the kinds of notice not to send. A kind this build does
	// not know mutes nothing, so a list written by a newer one still loads.
	Mute []string `json:"mute,omitempty"`
}

// NotifyEmail sends each notice as a mail through an SMTP server.
type NotifyEmail struct {
	Enabled bool `json:"enabled,omitempty"`
	// Server is host or host:port. The port follows Security when it is
	// left out.
	Server string `json:"server,omitempty"`
	// Security is how the connection is protected: SMTPStartTLS, the
	// default, SMTPTLS or SMTPNone.
	Security string   `json:"security,omitempty"`
	Username string   `json:"username,omitempty"`
	Password string   `json:"password,omitempty"`
	From     string   `json:"from,omitempty"`
	To       []string `json:"to,omitempty"`
}

// How a mail server connection is protected.
const (
	// SMTPStartTLS upgrades a plain connection, usually on port 587.
	SMTPStartTLS = "starttls"
	// SMTPTLS is TLS from the first byte, usually on port 465.
	SMTPTLS = "tls"
	// SMTPNone is a relay on the local network that takes mail as it is.
	SMTPNone = "none"
)

// SecurityOr is how the connection is protected, STARTTLS when unset.
func (e NotifyEmail) SecurityOr() string {
	if e.Security == "" {
		return SMTPStartTLS
	}
	return e.Security
}

// Address is the server's host:port, with the port Security implies when
// Server names none.
func (e NotifyEmail) Address() string {
	server := strings.TrimSpace(e.Server)
	if _, _, err := net.SplitHostPort(server); err == nil {
		return server
	}
	port := "587"
	switch e.SecurityOr() {
	case SMTPTLS:
		port = "465"
	case SMTPNone:
		port = "25"
	}
	return net.JoinHostPort(strings.Trim(server, "[]"), port)
}

// NotifyWebhook posts each notice to a URL.
type NotifyWebhook struct {
	Enabled bool `json:"enabled,omitempty"`
	// URL is https only. A chat service puts the key to its channel in it.
	URL string `json:"url,omitempty"`
	// Format is the shape of the body: WebhookJSON, the default, or one a
	// service reads as it is.
	Format string `json:"format,omitempty"`
	// Token is sent as a bearer token, for a receiver that asks for one.
	Token string `json:"token,omitempty"`
}

// Webhook body formats.
const (
	// WebhookJSON is Ostiole's own: the router and a list of notices.
	WebhookJSON = "json"
	// WebhookSlack is {"text": ...}, which Slack, Mattermost and Google
	// Chat read.
	WebhookSlack = "slack"
	// WebhookDiscord is {"content": ...}.
	WebhookDiscord = "discord"
	// WebhookNtfy is a plain text body with the title in a header, posted
	// to a topic.
	WebhookNtfy = "ntfy"
)

// FormatOr is the body format, JSON when unset.
func (w NotifyWebhook) FormatOr() string {
	if w.Format == "" {
		return WebhookJSON
	}
	return w.Format
}

// Muted reports whether notices of a kind are not sent.
func (n Notifications) Muted(kind string) bool {
	return slices.Contains(n.Mute, kind)
}

// Validate checks the block on its own, as a test of settings not yet
// applied does.
func (n Notifications) Validate() error {
	v := &validator{}
	v.notifications(&n)
	if len(v.issues) == 0 {
		return nil
	}
	return &ValidationError{Issues: v.issues}
}

var noticeKindRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)

// hasControl reports a character that could end a mail header early.
func hasControl(s string) bool {
	return strings.ContainsFunc(s, unicode.IsControl)
}

// notifications checks where notices go. The fields are checked whether a
// target is on or not, so the form can be filled in stages; what has to be
// there at all is only required once it is on.
func (v *validator) notifications(n *Notifications) {
	if n.Enabled && !n.Email.Enabled && !n.Webhook.Enabled {
		v.add("notifications.enabled", "turn on email, a webhook, or both")
	}
	for i, kind := range n.Mute {
		if !noticeKindRe.MatchString(kind) {
			v.add("notifications.mute["+strconv.Itoa(i)+"]", "%q is not a kind of notice", kind)
		}
	}

	e := &n.Email
	switch e.Security {
	case "", SMTPStartTLS, SMTPTLS, SMTPNone:
	default:
		v.add("notifications.email.security", "%q must be starttls, tls or none", e.Security)
	}
	if server := strings.TrimSpace(e.Server); server != "" {
		host, port, err := net.SplitHostPort(e.Address())
		n, perr := strconv.Atoi(port)
		switch {
		case err != nil || host == "" || hasControl(server) || strings.ContainsAny(server, " /@"):
			v.add("notifications.email.server", "%q is not a host or host:port", e.Server)
		case perr != nil || n < 1 || n > 65535:
			v.add("notifications.email.server", "%q has no port 1-65535", e.Server)
		}
	} else if e.Enabled {
		v.add("notifications.email.server", "say which mail server to send through")
	}
	if hasControl(e.Username) || hasControl(e.Password) {
		v.add("notifications.email.username", "the user name and password cannot hold control characters")
	}
	if e.SecurityOr() == SMTPNone && e.Password != "" {
		v.add("notifications.email.security", "a password over an unencrypted connection travels in the clear; use starttls or tls")
	}
	if e.From != "" {
		if _, err := mail.ParseAddress(e.From); err != nil || hasControl(e.From) {
			v.add("notifications.email.from", "%q is not an email address", e.From)
		}
	} else if e.Enabled {
		v.add("notifications.email.from", "say which address the mail comes from")
	}
	if e.Enabled && len(e.To) == 0 {
		v.add("notifications.email.to", "say who the mail goes to")
	}
	if len(e.To) > 20 {
		v.add("notifications.email.to", "%d recipients; 20 at most", len(e.To))
	}
	for i, to := range e.To {
		if _, err := mail.ParseAddress(to); err != nil || hasControl(to) {
			v.add("notifications.email.to["+strconv.Itoa(i)+"]", "%q is not an email address", to)
		}
	}

	w := &n.Webhook
	switch w.Format {
	case "", WebhookJSON, WebhookSlack, WebhookDiscord, WebhookNtfy:
	default:
		v.add("notifications.webhook.format", "%q must be json, slack, discord or ntfy", w.Format)
	}
	// The URL is not repeated back: a chat service's holds the key.
	if w.URL != "" {
		switch u, err := url.Parse(strings.TrimSpace(w.URL)); {
		case err != nil || hasControl(w.URL):
			v.add("notifications.webhook.url", "this is not a URL")
		case u.Scheme != "https":
			v.add("notifications.webhook.url", "the URL has to be https://")
		case u.Host == "":
			v.add("notifications.webhook.url", "the URL has no host name")
		case u.User != nil:
			v.add("notifications.webhook.url", "put credentials in the token, not the URL")
		}
	} else if w.Enabled {
		v.add("notifications.webhook.url", "say where to post")
	}
	if hasControl(w.Token) || strings.ContainsAny(w.Token, " ") {
		v.add("notifications.webhook.token", "the token cannot hold spaces or control characters")
	}
}
