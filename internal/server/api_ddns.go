package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/rforced/ostiole/internal/ddns"
	"github.com/rforced/ostiole/internal/model"
)

func (a *api) registerDDNS(mux *router) {
	mux.HandleFunc("GET /api/v1/ddns", a.read(a.ddnsStatus))
	mux.HandleFunc("POST /api/v1/ddns/check", a.write(a.ddnsCheck))
	mux.HandleFunc("POST /api/v1/ddns/{id}/update", a.write(a.ddnsUpdate))
	mux.HandleFunc("POST /api/v1/dns-providers/test", a.write(a.dnsProviderTest))
}

// errNoDDNS answers where nothing keeps the records, as in a test server.
var errNoDDNS = errors.New("nothing keeps dynamic DNS records on this router")

// ddnsResponse is the page: each record and type in the running
// configuration, and the kinds of DNS provider that can keep one.
type ddnsResponse struct {
	Records []ddns.Status        `json:"records"`
	Kinds   []model.ProviderKind `json:"kinds"`
}

func (a *api) ddnsStatus(w http.ResponseWriter, _ *http.Request) error {
	out := ddnsResponse{Records: []ddns.Status{}, Kinds: []model.ProviderKind{}}
	for _, k := range model.ProviderKinds {
		if k.DynamicDNS {
			out.Kinds = append(out.Kinds, k)
		}
	}
	if a.ddns != nil {
		out.Records = a.ddns.Status()
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// ddnsUpdate asks the provider about one record now, whatever was last
// found. It only starts the pass; the page reads the outcome from the
// status.
func (a *api) ddnsUpdate(w http.ResponseWriter, r *http.Request) error {
	if a.ddns == nil {
		return &unavailable{errNoDDNS}
	}
	switch err := a.ddns.UpdateNow(r.PathValue("id")); {
	case errors.Is(err, ddns.ErrUnknownRecord):
		return &notFound{err}
	case errors.Is(err, ddns.ErrRecordOff):
		return &badRequest{err}
	case err != nil:
		return err
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"id": r.PathValue("id")})
	return nil
}

type ddnsCheckRequest struct {
	Config *draftConfig      `json:"config"`
	Record *model.DDNSRecord `json:"record"`
}

// ddnsCheck reads what the provider holds for a record in a draft, with
// the draft's credentials, and says what an apply would do. Nothing is
// written.
func (a *api) ddnsCheck(w http.ResponseWriter, r *http.Request) error {
	if a.ddns == nil {
		return &unavailable{errNoDDNS}
	}
	var req ddnsCheckRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if req.Config == nil || req.Record == nil {
		return &badRequest{errors.New("config and record are required")}
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	results, err := a.ddns.Check(ctx, req.Config.config(), *req.Record)
	if err != nil {
		return &badRequest{err}
	}
	writeJSON(w, http.StatusOK, results)
	return nil
}

type providerTestRequest struct {
	Provider *model.DNSProvider `json:"provider"`
}

// dnsProviderTest tries a provider's credentials as the dialog holds them
// and reads each of its domains. Only a kind that keeps dynamic DNS
// records has a test; the others' clients only write challenge records.
func (a *api) dnsProviderTest(w http.ResponseWriter, r *http.Request) error {
	if a.ddns == nil {
		return &unavailable{errNoDDNS}
	}
	var req providerTestRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if req.Provider == nil {
		return &badRequest{errors.New("provider is required")}
	}
	kind, ok := model.ProviderKindOf(req.Provider.Kind)
	switch {
	case !ok:
		return &badRequest{fmt.Errorf("%q is not a DNS provider this build knows", req.Provider.Kind)}
	case !kind.DynamicDNS:
		return &badRequest{fmt.Errorf("testing a %s provider is not built in; %s can be tested", kind.Label, model.DynamicDNSKinds())}
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	res, err := a.ddns.TestProvider(ctx, *req.Provider)
	if err != nil {
		return &badRequest{err}
	}
	writeJSON(w, http.StatusOK, res)
	return nil
}
