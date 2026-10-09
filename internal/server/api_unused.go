package server

import (
	"fmt"
	"net/http"

	"ostiole/internal/model"
)

// unusedResponse is what a configuration leaves unused and what it has
// switched off (ADR-0042).
type unusedResponse struct {
	Unused   []model.Unused   `json:"unused"`
	Disabled []model.Disabled `json:"disabled"`
}

// unusedItems lists what nothing in a configuration uses. The body is the
// configuration itself, so the page can ask about its draft.
func (a *api) unusedItems(w http.ResponseWriter, r *http.Request) error {
	var cfg draftConfig
	if err := decodeJSON(r, &cfg); err != nil {
		return err
	}
	unused, disabled := cfg.config().Unused()
	writeJSON(w, http.StatusOK, unusedResponse{
		Unused:   append([]model.Unused{}, unused...),
		Disabled: append([]model.Disabled{}, disabled...),
	})
	return nil
}

// unusedWarning points at the Unused tab while the saved configuration
// has anything nothing uses. What is switched off is kept on purpose and
// never warns.
func unusedWarning(cfg *model.Config) (Warning, bool) {
	if cfg == nil {
		return Warning{}, false
	}
	unused, _ := cfg.Unused()
	if len(unused) == 0 {
		return Warning{}, false
	}
	items := "items"
	if len(unused) == 1 {
		items = "item"
	}
	return Warning{
		Kind: "unused", Level: "info", Key: "unused",
		Title:  "Unused configuration",
		Detail: fmt.Sprintf("Nothing uses %d %s. Details under System › Configuration › Unused.", len(unused), items),
	}, true
}
