package s3

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// The worked examples from the Amazon S3 API Reference, "Authenticating
// Requests: Using the Authorization Header (AWS Signature Version 4)".
// They are the only way to know the signer is right without a key.
const (
	exampleKeyID  = "AKIAIOSFODNN7EXAMPLE"
	exampleSecret = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	exampleHost   = "examplebucket.s3.amazonaws.com"
	exampleRegion = "us-east-1"
)

var exampleTime = time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)

func TestSignsThePublishedExamples(t *testing.T) {
	cases := []struct {
		name      string
		build     func() *http.Request
		payload   string
		canonical string
		toSign    string
		signature string
	}{
		{
			name: "GET Object",
			build: func() *http.Request {
				req, _ := http.NewRequest(http.MethodGet, "https://"+exampleHost+"/test.txt", nil)
				req.Header.Set("Range", "bytes=0-9")
				return req
			},
			payload: emptyPayload,
			canonical: `GET
/test.txt

host:examplebucket.s3.amazonaws.com
range:bytes=0-9
x-amz-content-sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
x-amz-date:20130524T000000Z

host;range;x-amz-content-sha256;x-amz-date
e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`,
			toSign: `AWS4-HMAC-SHA256
20130524T000000Z
20130524/us-east-1/s3/aws4_request
7344ae5b7ee6c3e7e6b0fe0640412a37625d1fbfff95c48bbb2dc43964946972`,
			signature: "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41",
		},
		{
			name: "PUT Object",
			build: func() *http.Request {
				body := strings.NewReader("Welcome to Amazon S3.")
				req, _ := http.NewRequest(http.MethodPut, "https://"+exampleHost+"/test$file.text", body)
				req.Header.Set("Date", "Fri, 24 May 2013 00:00:00 GMT")
				req.Header.Set("X-Amz-Storage-Class", "REDUCED_REDUNDANCY")
				return req
			},
			payload: "44ce7dd67c959e0d3524ffac1771dfbba87d2b6b4b4e99e42034a8b803f8b072",
			canonical: `PUT
/test%24file.text

date:Fri, 24 May 2013 00:00:00 GMT
host:examplebucket.s3.amazonaws.com
x-amz-content-sha256:44ce7dd67c959e0d3524ffac1771dfbba87d2b6b4b4e99e42034a8b803f8b072
x-amz-date:20130524T000000Z
x-amz-storage-class:REDUCED_REDUNDANCY

date;host;x-amz-content-sha256;x-amz-date;x-amz-storage-class
44ce7dd67c959e0d3524ffac1771dfbba87d2b6b4b4e99e42034a8b803f8b072`,
			toSign: `AWS4-HMAC-SHA256
20130524T000000Z
20130524/us-east-1/s3/aws4_request
9e0e90d9c76de8fa5b200d8c849cd5b8dc7a3be3951ddb7f6a76b4158342019d`,
			signature: "98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd",
		},
		{
			name: "GET Bucket Lifecycle",
			build: func() *http.Request {
				req, _ := http.NewRequest(http.MethodGet, "https://"+exampleHost+"/?lifecycle", nil)
				return req
			},
			payload: emptyPayload,
			canonical: `GET
/
lifecycle=
host:examplebucket.s3.amazonaws.com
x-amz-content-sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
x-amz-date:20130524T000000Z

host;x-amz-content-sha256;x-amz-date
e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`,
			toSign: `AWS4-HMAC-SHA256
20130524T000000Z
20130524/us-east-1/s3/aws4_request
9766c798316ff2757b517bc739a67f6213b4ab36dd5da2f94eaebf79c77395ca`,
			signature: "fea454ca298b7da1c68078a5d1bdbfbbe0d65c699e0f91ac7a200a0136783543",
		},
		{
			name: "GET Bucket (List Objects)",
			build: func() *http.Request {
				req, _ := http.NewRequest(http.MethodGet, "https://"+exampleHost+"/?max-keys=2&prefix=J", nil)
				return req
			},
			payload: emptyPayload,
			canonical: `GET
/
max-keys=2&prefix=J
host:examplebucket.s3.amazonaws.com
x-amz-content-sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
x-amz-date:20130524T000000Z

host;x-amz-content-sha256;x-amz-date
e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`,
			toSign: `AWS4-HMAC-SHA256
20130524T000000Z
20130524/us-east-1/s3/aws4_request
df57d21db20da04d7fa30298dd4488ba3a2b47ca3a489c74750e0f1e7df1b9b7`,
			signature: "34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := tc.build()
			req.Header.Set("X-Amz-Date", exampleTime.Format(stampFormat))
			req.Header.Set("X-Amz-Content-Sha256", tc.payload)
			names := signedNames(req.Header)
			canonical := canonicalRequest(req, names, tc.payload)
			if canonical != tc.canonical {
				t.Fatalf("canonical request:\n%s\nwant:\n%s", canonical, tc.canonical)
			}
			scope := exampleTime.Format(dateFormat) + "/" + exampleRegion + "/s3/aws4_request"
			if sts := stringToSign(exampleTime, scope, canonical); sts != tc.toSign {
				t.Fatalf("string to sign:\n%s\nwant:\n%s", sts, tc.toSign)
			}

			// And through the signer the client actually calls.
			signed := tc.build()
			sign(signed, exampleKeyID, exampleSecret, exampleRegion, tc.payload, exampleTime)
			auth := signed.Header.Get("Authorization")
			if !strings.HasSuffix(auth, "Signature="+tc.signature) {
				t.Errorf("Authorization = %q, want it to end in %q", auth, "Signature="+tc.signature)
			}
			if want := "Credential=" + exampleKeyID + "/" + scope; !strings.Contains(auth, want) {
				t.Errorf("Authorization = %q, want %q in it", auth, want)
			}
		})
	}
}

func TestVerifyAcceptsWhatSignWrote(t *testing.T) {
	body := []byte("a backup, encrypted")
	req, err := http.NewRequest(http.MethodPut, "https://s3.example.net/bucket/ostiole/router-20260920-030000.json.age", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	sign(req, "key", "secret", "us-east-1", hashOf(body), exampleTime)

	// What the service sees: no URL host on the request, a Host field,
	// and the body in hand.
	got := &http.Request{Method: req.Method, URL: req.URL, Host: req.URL.Host, Header: req.Header}
	if err := Verify(got, "key", "secret", body); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if err := Verify(got, "key", "another secret", body); err == nil {
		t.Error("a wrong secret verified")
	}
	if err := Verify(got, "key", "secret", []byte("something else")); err == nil {
		t.Error("a body that was not the one signed verified")
	}
}

func TestVerifyRefusesAnUnsignedHeader(t *testing.T) {
	req, err := http.NewRequest(http.MethodPut, "https://s3.example.net/bucket/key", nil)
	if err != nil {
		t.Fatal(err)
	}
	sign(req, "key", "secret", "us-east-1", emptyPayload, exampleTime)
	req.Header.Set("Content-MD5", "d41d8cd98f00b204e9800998ecf8427e")
	got := &http.Request{Method: req.Method, URL: req.URL, Host: req.URL.Host, Header: req.Header}
	if err := Verify(got, "key", "secret", nil); err == nil {
		t.Error("a header added after signing was accepted")
	}
}

func TestEncodesPathsAndQueriesTheS3Way(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"/bucket/ostiole/a b+c.json", "/bucket/ostiole/a%20b%2Bc.json"},
		{"/bucket/naïve", "/bucket/na%C3%AFve"},
		{"/bucket/test$file.text", "/bucket/test%24file.text"},
		{"", "/"},
	} {
		if got := encodePath(tc.in); got != tc.want {
			t.Errorf("encodePath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	for _, tc := range []struct{ in, want string }{
		{"prefix=b&list-type=2", "list-type=2&prefix=b"},
		{"lifecycle", "lifecycle="},
		{"continuation-token=a%2Fb%2Bc%3D", "continuation-token=a%2Fb%2Bc%3D"},
	} {
		if got := canonicalQuery(tc.in); got != tc.want {
			t.Errorf("canonicalQuery(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
