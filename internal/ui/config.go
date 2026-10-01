// The config page (#77 phase 1): a read-only YAML dump of the live,
// resolved config — exactly the bytes Config.Epoch() hashes, before
// truncation. It exists so an operator can answer "what is actually
// running" without shelling into the host to read arbiter.yaml, which on
// its own wouldn't even be enough: the served config is post
// env-expansion, post model-catalog-file merge, and post endpoint
// normalization.
//
// Deliberately just a YAML dump for now, not a structured/sectioned
// rendering: a sectioned template is a second place that must be kept in
// sync with every new config field (the same drift class the Overview
// KPI/drawer work went out of its way to avoid — see overview.go's
// Measures embedding). A more legible, documented rendering, and
// load-error localization (where + why a bad config was rejected), are
// tracked as later work in #77 and need their own design pass.
package ui

import (
	"net/http"

	"gopkg.in/yaml.v3"
)

// configView is the page's data. YAML is the redacted config's marshaled
// form — the same bytes Config.Epoch() hashes, so "what's on this page"
// and "what's the epoch a request was stamped with" can never disagree.
type configView struct {
	viewBase
	YAML     string
	Epoch    string
	NoConfig bool
}

// ConfigHandler handles GET /admin/ui/config: a read-only rendition of the
// live config. Unlike every other page here it does not depend on the event
// store — it reads the config the UI was wired with — so it renders even
// with storage.path unset.
func (h *Handler) ConfigHandler(w http.ResponseWriter, r *http.Request) {
	view := configView{viewBase: h.base(r.Context(), "Config")}
	view.Title = "config"

	cfg := h.cfg.Load()
	if cfg == nil {
		// Only reachable before the first SetConfig call — main.go always
		// calls it before serving, so this is a wiring bug elsewhere, not a
		// runtime state a real deployment hits. Shown rather than 500'd so
		// a test or a future caller that forgets the wiring gets a page
		// that explains itself.
		view.NoConfig = true
		h.render(w, r, "config", "config-body", view)
		return
	}

	raw, err := yaml.Marshal(cfg.Redacted())
	if err != nil {
		h.fail(w, r, http.StatusInternalServerError, "rendering config failed: "+err.Error())
		return
	}
	view.YAML = string(raw)
	view.Epoch = shortEpoch(cfg.Epoch())
	h.render(w, r, "config", "config-body", view)
}
