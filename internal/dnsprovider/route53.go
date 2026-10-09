package dnsprovider

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"ostiole/internal/dnsclient"
	"ostiole/internal/model"
	"ostiole/internal/sigv4"
)

// route53API is Route 53's global endpoint. China and GovCloud have their
// own, not built yet (TODO 24).
const route53API = "https://route53.amazonaws.com"

// route53Region is what a request to the global endpoint is signed for.
const route53Region = "us-east-1"

// route53TTL is what a challenge record set is made with.
const route53TTL = 10

// route53Poll is how often a change is asked after until it is INSYNC.
const route53Poll = 4 * time.Second

// route53Retries is how often a throttled or failed request goes again,
// as the AWS SDK's retryer did.
const route53Retries = 4

// route53Comment is written on each change batch.
const route53Comment = "ACME challenge from Ostiole"

// route53 writes records through Route 53's REST API with an access key.
// A name's values are one record set: a value is added or taken away by
// writing the set again, and each change is waited for.
type route53 struct {
	keyID, secret string
	// zoneID is the hosted zone the provider names; empty finds each zone
	// by name.
	zoneID    string
	base      string
	http      *http.Client
	userAgent string
	poll      time.Duration
	now       func() time.Time

	mu    sync.Mutex
	zones map[string]string
}

func newRoute53(p model.DNSProvider, o Options) (Client, error) {
	keyID, secret := strings.TrimSpace(p.Settings["accessKeyId"]), strings.TrimSpace(p.Settings["secretAccessKey"])
	if keyID == "" || secret == "" {
		return nil, errors.New("the Route 53 provider needs its access key ID and its secret access key")
	}
	region := strings.TrimSpace(p.Settings["region"])
	if strings.HasPrefix(region, "cn-") || strings.HasPrefix(region, "us-gov-") {
		return nil, &Error{Message: fmt.Sprintf("Route 53 in %s is not built in: Ostiole talks to the global endpoint only", region)}
	}
	return &route53{
		keyID: keyID, secret: secret,
		zoneID: strings.TrimPrefix(strings.TrimSpace(p.Settings["hostedZoneId"]), "/hostedzone/"),
		base:   route53API, http: o.client(), userAgent: o.UserAgent,
		poll: route53Poll, now: time.Now,
		zones: map[string]string{},
	}, nil
}

// r53Set is a record set as Route 53 writes and reads one.
type r53Set struct {
	Name    string      `xml:"Name"`
	Type    string      `xml:"Type"`
	TTL     int64       `xml:"TTL"`
	Records []r53Record `xml:"ResourceRecords>ResourceRecord"`
}

type r53Record struct {
	Value string `xml:"Value"`
}

type r53ChangeRequest struct {
	XMLName xml.Name    `xml:"https://route53.amazonaws.com/doc/2013-04-01/ ChangeResourceRecordSetsRequest"`
	Comment string      `xml:"ChangeBatch>Comment"`
	Changes []r53Change `xml:"ChangeBatch>Changes>Change"`
}

type r53Change struct {
	Action string `xml:"Action"`
	Set    r53Set `xml:"ResourceRecordSet"`
}

// r53ChangeInfo is the answer to a change and to asking after one.
type r53ChangeInfo struct {
	ID     string `xml:"ChangeInfo>Id"`
	Status string `xml:"ChangeInfo>Status"`
}

func (c *route53) AddTXT(ctx context.Context, r Record) error {
	id, err := c.zone(ctx, r.Zone)
	if err != nil {
		return err
	}
	name, value := dnsclient.FQDN(strings.ToLower(r.Name)), quoted(r.Value)
	set, err := c.set(ctx, id, name)
	if err != nil {
		return err
	}
	if set == nil {
		set = &r53Set{Name: name, Type: "TXT", TTL: route53TTL}
	}
	if slices.Contains(values(set), value) {
		return nil
	}
	set.Records = append(set.Records, r53Record{Value: value})
	return c.change(ctx, id, "UPSERT", *set)
}

func (c *route53) RemoveTXT(ctx context.Context, r Record) error {
	id, err := c.zone(ctx, r.Zone)
	if err != nil {
		return err
	}
	name, value := dnsclient.FQDN(strings.ToLower(r.Name)), quoted(r.Value)
	set, err := c.set(ctx, id, name)
	if err != nil || set == nil || !slices.Contains(values(set), value) {
		return err
	}
	// A DELETE must name the set exactly as it is, TTL included.
	if len(set.Records) == 1 {
		return c.change(ctx, id, "DELETE", *set)
	}
	set.Records = slices.DeleteFunc(set.Records, func(rec r53Record) bool { return rec.Value == value })
	return c.change(ctx, id, "UPSERT", *set)
}

func values(set *r53Set) []string {
	out := make([]string, 0, len(set.Records))
	for _, rec := range set.Records {
		out = append(out, rec.Value)
	}
	return out
}

// zone is the hosted zone's id: the one the provider names, or the public
// zone Route 53 holds by the zone's name.
func (c *route53) zone(ctx context.Context, zone string) (string, error) {
	if c.zoneID != "" {
		return c.zoneID, nil
	}
	name := dnsclient.FQDN(strings.ToLower(zone))
	c.mu.Lock()
	id, ok := c.zones[name]
	c.mu.Unlock()
	if ok {
		return id, nil
	}
	var out struct {
		Zones []struct {
			ID      string `xml:"Id"`
			Name    string `xml:"Name"`
			Private bool   `xml:"Config>PrivateZone"`
		} `xml:"HostedZones>HostedZone"`
	}
	q := url.Values{"dnsname": {strings.TrimSuffix(name, ".")}}
	if err := c.do(ctx, http.MethodGet, "/2013-04-01/hostedzonesbyname?"+q.Encode(), nil, &out); err != nil {
		return "", err
	}
	for _, z := range out.Zones {
		if strings.EqualFold(z.Name, name) && !z.Private {
			id = strings.TrimPrefix(z.ID, "/hostedzone/")
			c.mu.Lock()
			c.zones[name] = id
			c.mu.Unlock()
			return id, nil
		}
	}
	return "", &Error{Message: fmt.Sprintf("Route 53 holds no public hosted zone %s that this key can see", strings.TrimSuffix(name, "."))}
}

// set is the TXT record set at name, nil when there is none.
func (c *route53) set(ctx context.Context, zoneID, name string) (*r53Set, error) {
	var out struct {
		Sets []r53Set `xml:"ResourceRecordSets>ResourceRecordSet"`
	}
	q := url.Values{"name": {name}, "type": {"TXT"}, "maxitems": {"1"}}
	if err := c.do(ctx, http.MethodGet, "/2013-04-01/hostedzone/"+url.PathEscape(zoneID)+"/rrset?"+q.Encode(), nil, &out); err != nil {
		return nil, err
	}
	// The list starts at the name; the first set can be the next one.
	for _, s := range out.Sets {
		if strings.EqualFold(s.Name, name) && s.Type == "TXT" {
			return &s, nil
		}
	}
	return nil, nil
}

// change writes one change and waits until Route 53 says it is INSYNC.
func (c *route53) change(ctx context.Context, zoneID, action string, set r53Set) error {
	body, err := xml.Marshal(r53ChangeRequest{Comment: route53Comment, Changes: []r53Change{{Action: action, Set: set}}})
	if err != nil {
		return err
	}
	var info r53ChangeInfo
	if err := c.do(ctx, http.MethodPost, "/2013-04-01/hostedzone/"+url.PathEscape(zoneID)+"/rrset/", append([]byte(xml.Header), body...), &info); err != nil {
		return err
	}
	for info.Status != "INSYNC" {
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for Route 53 to apply %s: %w", info.ID, ctx.Err())
		case <-time.After(c.poll):
		}
		path := "/2013-04-01/change/" + url.PathEscape(strings.TrimPrefix(info.ID, "/change/"))
		if err := c.do(ctx, http.MethodGet, path, nil, &info); err != nil {
			return err
		}
	}
	return nil
}

// do sends one signed request and decodes the XML answer into out,
// sending a throttled or failed request again after a doubling wait.
func (c *route53) do(ctx context.Context, method, path string, body []byte, out any) error {
	for attempt := 0; ; attempt++ {
		status, answer, err := c.send(ctx, method, path, body)
		if err == nil && status/100 == 2 {
			if err := xml.Unmarshal(answer, out); err != nil {
				return fmt.Errorf("reading Route 53's answer: %w", err)
			}
			return nil
		}
		code := ""
		if err == nil {
			code, err = route53Refusal(status, answer)
		}
		again := status == 0 || status >= 500 || code == "Throttling" || code == "PriorRequestNotComplete"
		if !again || attempt >= route53Retries {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(time.Duration(200<<attempt) * time.Millisecond):
		}
	}
}

func (c *route53) send(ctx context.Context, method, path string, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/xml")
	}
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	sigv4.Sign(req, c.keyID, c.secret, route53Region, "route53", sigv4.HashOf(body), c.now())
	resp, err := c.http.Do(req)
	if err != nil {
		if ue, ok := errors.AsType[*url.Error](err); ok {
			err = ue.Err
		}
		return 0, nil, fmt.Errorf("could not reach Route 53: %w", err)
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer))
	if err != nil {
		return 0, nil, fmt.Errorf("reading Route 53's answer: %w", err)
	}
	return resp.StatusCode, answer, nil
}

// route53Refusal reads Route 53's error, which comes in two shapes: an
// ErrorResponse with a code, and InvalidChangeBatch with its messages.
func route53Refusal(status int, body []byte) (string, error) {
	var e struct {
		XMLName  xml.Name
		Code     string   `xml:"Error>Code"`
		Message  string   `xml:"Error>Message"`
		Messages []string `xml:"Messages>Message"`
	}
	detail := fmt.Sprintf("%d %s", status, http.StatusText(status))
	if xml.Unmarshal(body, &e) == nil {
		switch {
		case e.Code != "":
			detail = e.Code + ": " + strings.TrimSuffix(e.Message, ".")
		case len(e.Messages) > 0:
			detail = e.XMLName.Local + ": " + strings.TrimSuffix(strings.Join(e.Messages, "; "), ".")
		}
	}
	if status == http.StatusForbidden || status == http.StatusUnauthorized {
		return e.Code, &Error{Message: fmt.Sprintf("Route 53 refused the key (%s)", oneLine(detail))}
	}
	return e.Code, &Error{Message: "Route 53: " + oneLine(detail)}
}
