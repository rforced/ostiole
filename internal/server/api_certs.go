package server

import (
	"errors"
	"fmt"
	"net/http"
	"slices"

	"software.sslmate.com/src/go-pkcs12"

	"github.com/rforced/ostiole/internal/acme"
	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/certs"
	"github.com/rforced/ostiole/internal/model"
)

func (a *api) registerCerts(mux *router) {
	mux.HandleFunc("GET /api/v1/certificates", a.read(a.certificates))
	mux.HandleFunc("POST /api/v1/certificates/self-signed", a.admin(a.regenerateCertificate))
	mux.HandleFunc("POST /api/v1/certificates/keys", a.write(a.certificateKey))
	mux.HandleFunc("POST /api/v1/certificates/{id}/issue", a.admin(a.issueCertificate))
	mux.HandleFunc("GET /api/v1/certificates/{id}/files", a.certificateFiles(a.certificateBundle))
	mux.HandleFunc("GET /api/v1/certificates/{id}/files/{name}", a.certificateFiles(a.certificateFile))
	mux.HandleFunc("POST /api/v1/certificates/{id}/pkcs12", a.certificateFiles(a.certificatePKCS12))
}

// certificateResponse describes what the UI is talking to. Names is what
// the browser checks, so it is the field that explains most warnings.
type certificateResponse struct {
	*certs.Info
	// Managed is false when the server was started with certificate files
	// it does not own, in which case replacing them here is not offered.
	Managed bool `json:"managed"`
	// Hosts are the names a regenerated certificate would cover.
	Hosts []string `json:"hosts"`
}

// certificatesResponse is the page: the built-in pair, every certificate
// this router holds, and the DNS providers this build can write to.
type certificatesResponse struct {
	BuiltIn       *certificateResponse `json:"builtIn"`
	Certificates  []acme.Status        `json:"certificates"`
	ProviderKinds []model.ProviderKind `json:"providerKinds"`
}

func (a *api) certificates(w http.ResponseWriter, _ *http.Request) error {
	out := certificatesResponse{
		Certificates:  a.certificateStatus(),
		ProviderKinds: model.ProviderKinds,
	}
	if a.certs != nil {
		info, err := a.certs.Info()
		if err != nil && !errors.Is(err, certs.ErrNoCertificate) {
			return err
		}
		if info != nil {
			out.BuiltIn = &certificateResponse{Info: info, Managed: true, Hosts: a.hosts()}
		}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (a *api) certificateStatus() []acme.Status {
	if a.renewer == nil || a.engine == nil {
		return []acme.Status{}
	}
	return a.renewer.Status(a.engine.Effective())
}

func (a *api) hosts() []string {
	if a.certHosts == nil {
		return []string{}
	}
	return a.certHosts()
}

// regenerateCertificate makes a fresh self-signed certificate for the
// names this router currently answers to, which is what to do after moving
// it or renaming it.
func (a *api) regenerateCertificate(w http.ResponseWriter, r *http.Request) error {
	if a.certs == nil {
		return &unavailable{errors.New("this server is not serving HTTPS")}
	}
	var req struct {
		// Hosts overrides the detected names; empty uses them.
		Hosts []string `json:"hosts,omitempty"`
	}
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &req); err != nil {
			return err
		}
	}
	hosts := req.Hosts
	if len(hosts) == 0 {
		hosts = a.hosts()
	}
	if len(hosts) == 0 {
		return &badRequest{errors.New("no names to put in a certificate")}
	}
	info, err := a.certs.SelfSigned(hosts)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, certificateResponse{Info: info, Managed: true, Hosts: hosts})
	return nil
}

// certificateKey mints an account key for the ACME account dialog. The
// key is generated here so it never has to be pasted in.
func (a *api) certificateKey(w http.ResponseWriter, _ *http.Request) error {
	key, err := certs.GenerateAccountKey()
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]string{"privateKey": string(key)})
	return nil
}

// issueCertificate orders one now, whatever the schedule says. The order
// runs in the background: a CA can take a minute.
func (a *api) issueCertificate(w http.ResponseWriter, r *http.Request) error {
	if a.renewer == nil {
		return &unavailable{errors.New("this router does not issue certificates")}
	}
	id := r.PathValue("id")
	switch err := a.renewer.IssueNow(r.Context(), id); {
	case errors.Is(err, acme.ErrUploaded):
		return &badRequest{err}
	case err != nil:
		return err
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"id": id})
	return nil
}

// certificateBundle hands the files to whoever holds the certificate:
// another host over the API, or the browser's download menu.
func (a *api) certificateBundle(w http.ResponseWriter, r *http.Request) error {
	f, err := a.readCertificate(r.PathValue("id"))
	if err != nil {
		return err
	}
	out := map[string]any{
		"certificate": string(f.FullChain),
		"chain":       string(f.Chain),
		"key":         string(f.Key),
		"notAfter":    f.State.NotAfter,
	}
	if info, err := certs.Describe(f.Cert); err == nil {
		out["notAfter"], out["fingerprint"] = info.NotAfter, info.Fingerprint
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// certificateFile hands out one PEM file as a download.
func (a *api) certificateFile(w http.ResponseWriter, r *http.Request) error {
	name := r.PathValue("name")
	if !slices.Contains([]string{certs.CertFile, certs.ChainFile, certs.FullChainFile, certs.KeyFile}, name) {
		return &badRequest{fmt.Errorf("no file called %q", name)}
	}
	f, err := a.readCertificate(r.PathValue("id"))
	if err != nil {
		return err
	}
	var body []byte
	switch name {
	case certs.CertFile:
		body = f.Cert
	case certs.ChainFile:
		body = f.Chain
	case certs.FullChainFile:
		body = f.FullChain
	case certs.KeyFile:
		body = f.Key
	}
	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+r.PathValue("id")+"-"+name+"\"")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
	return nil
}

// certificatePKCS12 packs the certificate and its key into the one file
// Windows and Java will read.
func (a *api) certificatePKCS12(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	f, err := a.readCertificate(r.PathValue("id"))
	if err != nil {
		return err
	}
	pair, err := certs.KeyPair(f)
	if err != nil {
		return &badRequest{err}
	}
	out, err := pkcs12.Modern2023.Encode(pair.Key, pair.Leaf, pair.Chain, req.Password)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/x-pkcs12")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+r.PathValue("id")+".p12\"")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out) //nolint:gosec // G705: a binary attachment, nosniff, never rendered
	return nil
}

// readCertificate says which of two absences it found: a certificate the
// configuration knows and has not issued yet, or no such certificate.
func (a *api) readCertificate(id string) (*certs.Files, error) {
	if a.certStore == nil {
		return nil, &unavailable{errors.New("this router holds no certificates")}
	}
	f, err := a.certStore.Read(id)
	if !errors.Is(err, certs.ErrNoCertificate) {
		return f, err
	}
	if a.engine != nil {
		if cfg := a.engine.Effective(); cfg != nil {
			if _, ok := cfg.Certificate(id); ok {
				return nil, &notFound{fmt.Errorf("certificate %q has not been issued yet", id)}
			}
		}
	}
	return nil, &notFound{fmt.Errorf("no certificate called %q", id)}
}

// notFound is a 404 that says what is missing.
type notFound struct{ err error }

func (e *notFound) Error() string { return e.err.Error() }
func (e *notFound) Unwrap() error { return e.err }

// certificateFiles wraps the routes that hand out a certificate's files:
// an administrator, or a token restricted to the certificate in the path.
// It takes no engine, so a token still fetches during a pending apply.
func (a *api) certificateFiles(h func(w http.ResponseWriter, r *http.Request) error) http.HandlerFunc {
	return a.public(func(w http.ResponseWriter, r *http.Request) error {
		if a.auth == nil {
			return &unavailable{errors.New("authentication not available")}
		}
		p, ok := a.authenticate(r)
		if !ok {
			return errUnauthorized
		}
		if len(p.Certificates) > 0 {
			if !slices.Contains(p.Certificates, r.PathValue("id")) {
				return errForbidden
			}
			return h(w, r)
		}
		if !p.Role.Allows(auth.RoleAdmin) {
			return errForbidden
		}
		return h(w, r)
	})
}
