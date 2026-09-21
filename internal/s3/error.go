package s3

import (
	"encoding/xml"
	"fmt"
	"net/http"
)

// The operations an error can name. They are written into a sentence, so
// they read as the thing the key was not allowed to do.
const (
	OpUpload    = "upload"
	OpDownload  = "download"
	OpList      = "list"
	OpDelete    = "delete"
	OpReadRule  = "read the retention rule"
	OpWriteRule = "write the retention rule"
)

// Error is what the service said, as a sentence. The codes worth
// translating are the ones an operator can do something about: a wrong
// clock, a wrong key, a missing bucket, a capability the key was not
// given.
type Error struct {
	Status  int
	Code    string
	Message string
	// Op is what was being attempted, for a refusal that names no
	// detail of its own.
	Op string
	// Bucket names the bucket the request was for.
	Bucket string
	// Endpoint is where a redirect said the bucket lives, when it said.
	Endpoint string
}

func (e *Error) Error() string {
	switch e.Code {
	case "RequestTimeTooSkewed":
		return "the service rejected the request time; the router's clock is wrong"
	case "InvalidAccessKeyId", "SignatureDoesNotMatch":
		return "the service refused the key"
	case "AccessDenied":
		return "the key may not " + e.Op
	case "NoSuchBucket":
		return "there is no bucket called " + e.Bucket
	case "NoSuchKey":
		return "that copy is no longer in the bucket"
	case "NotImplemented":
		return "the service cannot " + e.Op
	case "PermanentRedirect", "TemporaryRedirect":
		if e.Endpoint != "" {
			return "the bucket is served from " + e.Endpoint + ", not this endpoint"
		}
		return "the bucket is served from another endpoint; check the endpoint and the region"
	}
	switch {
	case e.Code != "" && e.Message != "":
		return fmt.Sprintf("%s: %s (HTTP %d)", e.Code, e.Message, e.Status)
	case e.Code != "":
		return fmt.Sprintf("%s (HTTP %d)", e.Code, e.Status)
	case e.Message != "":
		return fmt.Sprintf("%s (HTTP %d)", e.Message, e.Status)
	}
	return fmt.Sprintf("the service answered %d %s", e.Status, http.StatusText(e.Status))
}

// errorFrom reads the S3 error document. A body that is not one keeps
// the status text, so a proxy's HTML page does not become the message.
func (c *Client) errorFrom(status int, op string, body []byte) *Error {
	e := &Error{Status: status, Op: op, Bucket: c.Bucket}
	var doc struct {
		XMLName  xml.Name `xml:"Error"`
		Code     string   `xml:"Code"`
		Message  string   `xml:"Message"`
		Endpoint string   `xml:"Endpoint"`
	}
	if err := xml.Unmarshal(body, &doc); err == nil {
		e.Code, e.Message, e.Endpoint = doc.Code, doc.Message, doc.Endpoint
	}
	return e
}
