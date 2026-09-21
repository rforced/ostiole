package s3

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // Content-MD5 is what the API asks for here; it is not a security claim.
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"strconv"
)

// lifecycleNS is the namespace the rules are written in. The rules kept
// verbatim from the service were read under it, so they are written back
// under it too.
const lifecycleNS = "http://s3.amazonaws.com/doc/2006-03-01/"

// Lifecycle keeps every rule as the XML it arrived in, so the rules
// Ostiole does not own go back exactly as they were.
type Lifecycle struct{ Rules []Rule }

// Rule is one rule of a bucket's lifecycle configuration.
type Rule struct {
	ID string
	// XML is the inner XML of <Rule>, verbatim.
	XML []byte
}

// Rule returns the rule with an id.
func (l *Lifecycle) Rule(id string) (Rule, bool) {
	for _, r := range l.Rules {
		if r.ID == id {
			return r, true
		}
	}
	return Rule{}, false
}

// Set replaces the rule with the same id, or appends it.
func (l *Lifecycle) Set(r Rule) {
	for i := range l.Rules {
		if l.Rules[i].ID == r.ID {
			l.Rules[i] = r
			return
		}
	}
	l.Rules = append(l.Rules, r)
}

// Remove drops a rule by id and says whether it was there.
func (l *Lifecycle) Remove(id string) bool {
	for i := range l.Rules {
		if l.Rules[i].ID == id {
			l.Rules = append(l.Rules[:i], l.Rules[i+1:]...)
			return true
		}
	}
	return false
}

// Expiry is the part of a rule Ostiole writes: how long a bucket keeps
// what is under a prefix. Comparing these rather than the XML is what
// makes a second run write nothing, because a service stores the rule in
// its own words.
type Expiry struct {
	Prefix string
	// Days is how long an object lives; zero is forever.
	Days int
	// NoncurrentDays is how long a hidden version lives. On a bucket that
	// keeps versions a delete only hides, and this is what frees the
	// space afterwards.
	NoncurrentDays int
	// Status is Enabled or Disabled.
	Status string
	// DeleteMarker removes a hide marker that is the last version left.
	// It is a dialect rather than a policy: Amazon refuses a rule that
	// has it beside Days, Backblaze refuses one without it, and no
	// document satisfies both. Matches ignores it for that reason.
	DeleteMarker bool
}

// Matches reports whether two rules say the same thing. The dialect a
// service stores the rule in is not part of the comparison; the policy
// is.
func (e Expiry) Matches(other Expiry) bool {
	e.DeleteMarker, other.DeleteMarker = false, false
	return e == other
}

// Enabled is the only status a rule Ostiole writes has; several services
// refuse a disabled one.
const Enabled = "Enabled"

// ExpiryRule renders a rule. The element order is the one the S3 schema
// lists, which the stricter services insist on.
func ExpiryRule(id string, e Expiry) Rule {
	if e.Status == "" {
		e.Status = Enabled
	}
	var b bytes.Buffer
	element(&b, "ID", id)
	b.WriteString("<Filter>")
	element(&b, "Prefix", e.Prefix)
	b.WriteString("</Filter>")
	element(&b, "Status", e.Status)
	if e.Days > 0 || e.DeleteMarker {
		b.WriteString("<Expiration>")
		if e.Days > 0 {
			element(&b, "Days", strconv.Itoa(e.Days))
		}
		if e.DeleteMarker {
			element(&b, "ExpiredObjectDeleteMarker", "true")
		}
		b.WriteString("</Expiration>")
	}
	if e.NoncurrentDays > 0 {
		b.WriteString("<NoncurrentVersionExpiration>")
		element(&b, "NoncurrentDays", strconv.Itoa(e.NoncurrentDays))
		b.WriteString("</NoncurrentVersionExpiration>")
	}
	return Rule{ID: id, XML: b.Bytes()}
}

// Expiry reads back what ExpiryRule writes, and enough of a rule written
// elsewhere to compare with one. The prefix comes from either the filter
// or the older bare element.
func (r Rule) Expiry() (Expiry, error) {
	var doc struct {
		XMLName xml.Name `xml:"Rule"`
		Prefix  string   `xml:"Prefix"`
		Filter  struct {
			Prefix string `xml:"Prefix"`
		} `xml:"Filter"`
		Status     string `xml:"Status"`
		Expiration struct {
			Days         int  `xml:"Days"`
			DeleteMarker bool `xml:"ExpiredObjectDeleteMarker"`
		} `xml:"Expiration"`
		Noncurrent struct {
			Days int `xml:"NoncurrentDays"`
		} `xml:"NoncurrentVersionExpiration"`
	}
	wrapped := append(append([]byte("<Rule>"), r.XML...), "</Rule>"...)
	if err := xml.Unmarshal(wrapped, &doc); err != nil {
		return Expiry{}, fmt.Errorf("the rule %q could not be read: %w", r.ID, err)
	}
	prefix := doc.Filter.Prefix
	if prefix == "" {
		prefix = doc.Prefix
	}
	return Expiry{
		Prefix:         prefix,
		Days:           doc.Expiration.Days,
		NoncurrentDays: doc.Noncurrent.Days,
		Status:         doc.Status,
		DeleteMarker:   doc.Expiration.DeleteMarker,
	}, nil
}

func element(b *bytes.Buffer, name, value string) {
	b.WriteString("<" + name + ">")
	_ = xml.EscapeText(b, []byte(value))
	b.WriteString("</" + name + ">")
}

// Lifecycle reads the bucket's rules. A bucket with none is empty rather
// than an error: that is the ordinary state of a new bucket.
func (c *Client) Lifecycle(ctx context.Context) (*Lifecycle, error) {
	body, err := c.do(ctx, request{
		method: http.MethodGet, query: "lifecycle", op: OpReadRule, limit: maxListBytes,
	})
	var serr *Error
	if errors.As(err, &serr) &&
		(serr.Code == "NoSuchLifecycleConfiguration" || (serr.Status == http.StatusNotFound && serr.Code == "")) {
		return &Lifecycle{}, nil
	}
	if err != nil {
		return nil, err
	}
	var doc struct {
		XMLName xml.Name `xml:"LifecycleConfiguration"`
		Rules   []struct {
			ID    string `xml:"ID"`
			Inner []byte `xml:",innerxml"`
		} `xml:"Rule"`
	}
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("the retention rules the service sent could not be read: %w", err)
	}
	out := &Lifecycle{}
	for _, r := range doc.Rules {
		out.Rules = append(out.Rules, Rule{ID: r.ID, XML: r.Inner})
	}
	return out, nil
}

// PutLifecycle replaces the bucket's whole rule set.
func (c *Client) PutLifecycle(ctx context.Context, l *Lifecycle) error {
	var b bytes.Buffer
	b.WriteString(`<LifecycleConfiguration xmlns="` + lifecycleNS + `">`)
	for _, r := range l.Rules {
		b.WriteString("<Rule>")
		b.Write(r.XML)
		b.WriteString("</Rule>")
	}
	b.WriteString("</LifecycleConfiguration>")
	body := b.Bytes()
	sum := md5.Sum(body) //nolint:gosec // Content-MD5 is what the API asks for here; it is not a security claim.
	_, err := c.do(ctx, request{
		method: http.MethodPut, query: "lifecycle", body: body, contentType: "application/xml",
		headers: map[string]string{"Content-MD5": base64.StdEncoding.EncodeToString(sum[:])},
		op:      OpWriteRule,
	})
	return err
}

// DeleteLifecycle removes every rule from the bucket, which is only ever
// done when Ostiole's was the last one left.
func (c *Client) DeleteLifecycle(ctx context.Context) error {
	_, err := c.do(ctx, request{method: http.MethodDelete, query: "lifecycle", op: OpWriteRule})
	return err
}
