package engine

import (
	"encoding/json"
	"time"

	"github.com/rforced/ostiole/internal/store"
)

// FallbackStatus says the kernel runs the fallback ruleset, since when, and
// why the saved one did not load.
type FallbackStatus struct {
	Since  time.Time `json:"since"`
	Reason string    `json:"reason"`
}

func (e *Engine) readFallback() *FallbackStatus {
	raw, err := e.store.ReadState(store.FallbackFile)
	if err != nil {
		return nil
	}
	var f FallbackStatus
	if json.Unmarshal(raw, &f) != nil {
		return &FallbackStatus{Reason: "unknown"}
	}
	return &f
}

func (e *Engine) noteFallback(why error) {
	raw, err := json.Marshal(FallbackStatus{Since: time.Now(), Reason: why.Error()})
	if err == nil {
		err = e.store.WriteState(store.FallbackFile, raw)
	}
	if err != nil {
		e.log.Warn("could not record that the fallback ruleset is loaded", "err", err)
	}
}

func (e *Engine) clearFallback() {
	if err := e.store.RemoveState(store.FallbackFile); err != nil {
		e.log.Warn("could not clear the fallback note", "err", err)
	}
}
