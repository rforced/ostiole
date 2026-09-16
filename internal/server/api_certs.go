package server

import (
	"errors"
	"net/http"

	"github.com/rforced/ostiole/internal/certs"
)

func (a *api) registerCerts(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/certificate", a.guard(a.certificate))
	mux.HandleFunc("POST /api/v1/certificate", a.guard(a.installCertificate))
	mux.HandleFunc("POST /api/v1/certificate/self-signed", a.guard(a.regenerateCertificate))
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

func (a *api) certificate(w http.ResponseWriter, _ *http.Request) error {
	if a.certs == nil {
		return &unavailable{errors.New("this server is not serving TLS")}
	}
	info, err := a.certs.Info()
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, certificateResponse{Info: info, Managed: true, Hosts: a.hosts()})
	return nil
}

func (a *api) hosts() []string {
	if a.certHosts == nil {
		return []string{}
	}
	return a.certHosts()
}

// installCertificate replaces the certificate with one the operator got
// elsewhere. It takes effect on the next connection, so the browser that
// uploaded it is the first to see the new one.
func (a *api) installCertificate(w http.ResponseWriter, r *http.Request) error {
	if a.certs == nil {
		return &unavailable{errors.New("this server is not serving TLS")}
	}
	var req struct {
		Certificate string `json:"certificate"`
		Key         string `json:"key"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if req.Certificate == "" || req.Key == "" {
		return &badRequest{errors.New("both the certificate and its private key are required")}
	}
	info, err := a.certs.Install([]byte(req.Certificate), []byte(req.Key))
	if err != nil {
		return &badRequest{err}
	}
	writeJSON(w, http.StatusOK, certificateResponse{Info: info, Managed: true, Hosts: a.hosts()})
	return nil
}

// regenerateCertificate makes a fresh self-signed certificate for the
// names this box currently answers to, which is what to do after moving
// it or renaming it.
func (a *api) regenerateCertificate(w http.ResponseWriter, r *http.Request) error {
	if a.certs == nil {
		return &unavailable{errors.New("this server is not serving TLS")}
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
