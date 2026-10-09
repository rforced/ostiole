package dnsprovider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"ostiole/internal/model"
)

// hetznerAPI is the root of Hetzner's Cloud API, which holds DNS zones
// since the DNS console moved there.
const hetznerAPI = "https://api.hetzner.cloud/v1"

// hetznerTTL is what a challenge record is written with.
const hetznerTTL = 60

// hetznerPoll is how often an action is asked after.
const hetznerPoll = 2 * time.Second

// hetzner writes records through Hetzner's Cloud API. Adding a value to a
// name's set and taking one away are actions that finish later; each is
// waited for.
type hetzner struct {
	api
	poll time.Duration
}

func newHetzner(p model.DNSProvider, o Options) (Client, error) {
	token := strings.TrimSpace(p.Settings["token"])
	if token == "" {
		return nil, errors.New("the Hetzner provider has no API token")
	}
	return &hetzner{
		name: "Hetzner", base: hetznerAPI, http: o.client(), userAgent: o.UserAgent,
		auth:   func(h http.Header) { h.Set("Authorization", "Bearer "+token) },
		detail: hetznerDetail,
		poll:   hetznerPoll,
	}, nil
}

// hetznerAction is a change Hetzner carries out on its own time.
type hetznerAction struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type hetznerRecord struct {
	Value string `json:"value"`
}

func (h *hetzner) AddTXT(ctx context.Context, r Record) error {
	body := map[string]any{"ttl": hetznerTTL, "records": []hetznerRecord{{Value: quoted(r.Value)}}}
	_, err := h.act(ctx, r, "add_records", body)
	return err
}

func (h *hetzner) RemoveTXT(ctx context.Context, r Record) error {
	body := map[string]any{"records": []hetznerRecord{{Value: quoted(r.Value)}}}
	status, err := h.act(ctx, r, "remove_records", body)
	// A set that is not there holds nothing to take away.
	if status == http.StatusNotFound {
		return nil
	}
	return err
}

// act starts one action on the name's TXT set and waits for it to end.
func (h *hetzner) act(ctx context.Context, r Record, action string, body any) (int, error) {
	sub, err := relative(r.Name, r.Zone)
	if err != nil {
		return 0, err
	}
	if sub == "" {
		sub = "@"
	}
	path := "/zones/" + url.PathEscape(r.Zone) + "/rrsets/" + url.PathEscape(sub) + "/TXT/actions/" + action
	var out struct {
		Action hetznerAction `json:"action"`
	}
	status, err := h.do(ctx, http.MethodPost, path, body, &out)
	if err != nil {
		return status, err
	}
	a := out.Action
	for a.Status == "running" {
		select {
		case <-ctx.Done():
			return status, fmt.Errorf("waiting for Hetzner to finish %s for %s: %w", action, r.Name, ctx.Err())
		case <-time.After(h.poll):
		}
		out.Action = hetznerAction{}
		if _, err := h.do(ctx, http.MethodGet, "/actions/"+strconv.FormatInt(a.ID, 10), nil, &out); err != nil {
			return status, err
		}
		a = out.Action
	}
	if a.Status == "error" {
		msg := "the action failed"
		if a.Error != nil && a.Error.Message != "" {
			msg = strings.TrimSuffix(a.Error.Message, ".")
		}
		return status, &Error{Message: fmt.Sprintf("Hetzner: %s for %s: %s", action, r.Name, msg)}
	}
	return status, nil
}

// hetznerDetail reads Hetzner's error: a code and a message.
func hetznerDetail(body []byte) string {
	var e struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		return e.Error.Code + ": " + strings.TrimSuffix(e.Error.Message, ".")
	}
	return oneLine(string(body))
}
