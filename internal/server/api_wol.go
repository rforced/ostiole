package server

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"syscall"

	"github.com/rforced/ostiole/internal/wol"
)

func (a *api) registerWoL(mux *router) {
	mux.HandleFunc("POST /api/v1/wol/wake", a.write(a.wolWake))
}

type wakeRequest struct {
	Interface string `json:"interface"`
	MAC       string `json:"mac"`
}

// wolWake sends a magic packet onto one of the router's inside networks.
// Any machine may be named, saved or not. The interface is checked against
// the configuration in force, since one that is only in a draft does not
// exist yet.
func (a *api) wolWake(w http.ResponseWriter, r *http.Request) error {
	var req wakeRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	cfg := a.engine.Effective()
	if cfg == nil {
		return &badRequest{errors.New("nothing has been applied yet")}
	}
	if _, ok := cfg.Interface(req.Interface); !ok {
		return &badRequest{fmt.Errorf("interface %q is not in the applied configuration", req.Interface)}
	}
	mac, err := cfg.WakeTarget(req.Interface, req.MAC)
	if err != nil {
		return &badRequest{err}
	}
	send := a.wake
	if send == nil {
		send = wol.Send
	}
	if err := send(req.Interface, mac); err != nil {
		return wakeError(req.Interface, err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// wakeError says what stopped a wake: a daemon without the privilege to
// send one, or a link that is down or missing.
func wakeError(iface string, err error) error {
	switch {
	case errors.Is(err, fs.ErrPermission):
		return &unavailable{errors.New("sending a wake needs the daemon to run as root")}
	case errors.Is(err, syscall.ENETDOWN):
		return &badRequest{fmt.Errorf("interface %q is down", iface)}
	case errors.Is(err, wol.ErrNoLink):
		return &badRequest{fmt.Errorf("interface %q is not on this router", iface)}
	}
	return err
}
