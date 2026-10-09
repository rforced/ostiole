package dnsprovider

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"ostiole/internal/model"
)

// porkbunAPI is the root of Porkbun's v3 JSON API.
const porkbunAPI = "https://api.porkbun.com/api/json/v3"

// porkbunTTL is what a challenge record is written with: Porkbun's
// minimum.
const porkbunTTL = 300

// porkbun writes records through Porkbun's API, a record each. Every call
// is a POST with both keys in the body. A record's id is kept from when
// it was made; one made before a restart is found by name and value.
type porkbun struct {
	api
	apiKey, secretKey string

	mu  sync.Mutex
	ids map[Record]string
}

func newPorkbun(p model.DNSProvider, o Options) (Client, error) {
	apiKey, secretKey := strings.TrimSpace(p.Settings["apiKey"]), strings.TrimSpace(p.Settings["secretKey"])
	if apiKey == "" || secretKey == "" {
		return nil, errors.New("the Porkbun provider needs its API key and its secret key")
	}
	return &porkbun{
		name: "Porkbun", base: porkbunAPI, http: o.client(), userAgent: o.UserAgent, detail: porkbunDetail,
		apiKey: apiKey, secretKey: secretKey,
		ids: map[Record]string{},
	}, nil
}

// porkbunID is a record's id, which comes back as a number from a create
// and as a string from a lookup.
type porkbunID string

func (id *porkbunID) UnmarshalJSON(b []byte) error {
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*id = porkbunID(n)
	return nil
}

// porkbunStatus is what every answer carries.
type porkbunStatus struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

func (p *porkbun) AddTXT(ctx context.Context, r Record) error {
	sub, err := relative(r.Name, r.Zone)
	if err != nil {
		return err
	}
	var out struct {
		porkbunStatus
		ID porkbunID `json:"id"`
	}
	body := map[string]any{"name": sub, "type": "TXT", "content": r.Value, "ttl": strconv.Itoa(porkbunTTL)}
	if err := p.call(ctx, "/dns/create/"+url.PathEscape(r.Zone), body, &out, &out.porkbunStatus); err != nil {
		return err
	}
	p.mu.Lock()
	p.ids[key(r)] = string(out.ID)
	p.mu.Unlock()
	return nil
}

func (p *porkbun) RemoveTXT(ctx context.Context, r Record) error {
	p.mu.Lock()
	id, known := p.ids[key(r)]
	p.mu.Unlock()
	ids := []string{id}
	if !known {
		var err error
		if ids, err = p.find(ctx, r); err != nil {
			return err
		}
	}
	for _, id := range ids {
		var out porkbunStatus
		if err := p.call(ctx, "/dns/delete/"+url.PathEscape(r.Zone)+"/"+url.PathEscape(id), nil, &out, &out); err != nil {
			return err
		}
	}
	p.mu.Lock()
	delete(p.ids, key(r))
	p.mu.Unlock()
	return nil
}

// find lists the name's TXT records holding the value.
func (p *porkbun) find(ctx context.Context, r Record) ([]string, error) {
	sub, err := relative(r.Name, r.Zone)
	if err != nil {
		return nil, err
	}
	path := "/dns/retrieveByNameType/" + url.PathEscape(r.Zone) + "/TXT"
	if sub != "" {
		path += "/" + url.PathEscape(sub)
	}
	var out struct {
		porkbunStatus
		Records []struct {
			ID      porkbunID `json:"id"`
			Content string    `json:"content"`
		} `json:"records"`
	}
	if err := p.call(ctx, path, nil, &out, &out.porkbunStatus); err != nil {
		return nil, err
	}
	var ids []string
	for _, rec := range out.Records {
		if rec.Content == r.Value {
			ids = append(ids, string(rec.ID))
		}
	}
	return ids, nil
}

// call posts the keys and body to path and reads the answer into out,
// whose status says whether it worked.
func (p *porkbun) call(ctx context.Context, path string, body map[string]any, out any, st *porkbunStatus) error {
	req := map[string]any{"apikey": p.apiKey, "secretapikey": p.secretKey}
	maps.Copy(req, body)
	if _, err := p.do(ctx, http.MethodPost, path, req, out); err != nil {
		return err
	}
	if st.Status != "SUCCESS" {
		msg := strings.TrimSuffix(st.Message, ".")
		if msg == "" {
			msg = "the request failed"
		}
		return &Error{Message: "Porkbun: " + msg}
	}
	return nil
}

// key is what a record's id is kept under: the record without the
// challenge it answers.
func key(r Record) Record {
	return Record{Zone: strings.ToLower(r.Zone), Name: strings.ToLower(r.Name), Value: r.Value}
}

// porkbunDetail reads Porkbun's error: a message.
func porkbunDetail(body []byte) string {
	var e porkbunStatus
	if json.Unmarshal(body, &e) == nil && e.Message != "" {
		return strings.TrimSuffix(e.Message, ".")
	}
	return oneLine(string(body))
}
