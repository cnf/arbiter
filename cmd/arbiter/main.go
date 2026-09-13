package main

import (
	"cmp"
	"context"
	"flag"
	"fmt"
	"log/slog"
	stdhttp "net/http"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"

	"github.com/gorilla/mux"

	"github.com/cnf/arbiter/internal/classifier"
	"github.com/cnf/arbiter/internal/config"
	"github.com/cnf/arbiter/internal/guardrail"
	arbiterhttp "github.com/cnf/arbiter/internal/http"
	"github.com/cnf/arbiter/internal/logging"
	"github.com/cnf/arbiter/internal/pipeline"
	"github.com/cnf/arbiter/internal/router"
	"github.com/cnf/arbiter/internal/translator"
	"github.com/cnf/arbiter/internal/upstream"
	"github.com/cnf/arbiter/pkg/types"
)

func main() {
	configPath := flag.String("config", "lanes.yaml", "Path to lanes.yaml config file")
	port := flag.String("port", "8080", "Port to listen on")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	logger := logging.NewStdoutLogger(cfg.Logging.Level)
	slog.Info("Arbiter starting", "config", *configPath, "port", *port)

	p, err := buildPipeline(cfg, logger)
	if err != nil {
		slog.Error("failed to build pipeline", "error", err)
		os.Exit(1)
	}
	handler := arbiterhttp.NewHandler(p, logger, configuredModels(cfg))

	r := mux.NewRouter()
	r.HandleFunc("/v1/messages", handler.MessagesHandler).Methods("POST")
	r.HandleFunc("/chat/completions", handler.CompletionsHandler).Methods("POST")
	r.HandleFunc("/models", handler.ModelsHandler).Methods("GET")
	r.HandleFunc("/v1/models", handler.ModelsHandler).Methods("GET")
	r.HandleFunc("/health", func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := fmt.Fprint(w, `{"status":"ok"}`); err != nil {
			slog.Error("health response failed", "error", err)
		}
	}).Methods("GET")

	srv := &stdhttp.Server{
		Addr:         ":" + *port,
		Handler:      r,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	go func() {
		slog.Info("listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && err != stdhttp.ErrServerClosed {
			slog.Error("server error", "error", err)
		}
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	slog.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("shutdown error", "error", err)
		os.Exit(1)
	}

	slog.Info("shutdown complete")
}

// buildPipeline turns config into a fully wired Pipeline: provider table,
// translator, classifiers, router, guardrails, upstream client. This is the
// one place that knows how config type-names map to concrete constructors —
// adding a new classifier/router/guardrail type means adding a case here
// (or, once there's a reason to, registering it into router.Registry /
// classifier.Registry / guardrail.Registry instead of switching on it).
func buildPipeline(cfg *config.Config, logger logging.Logger) (*pipeline.Pipeline, error) {
	providers := make(map[string]types.ProviderConfig, len(cfg.Providers))
	for name, pc := range cfg.Providers {
		timeout := 60 * time.Second
		if pc.Timeout != "" {
			if d, err := time.ParseDuration(pc.Timeout); err == nil {
				timeout = d
			}
		}
		providers[name] = types.ProviderConfig{
			Name:     name,
			Type:     pc.Type,
			Endpoint: pc.Endpoint,
			APIKey:   pc.Key,
			Models:   pc.Models,
			Headers:  pc.Headers,
			Timeout:  timeout,
			RetryMax: pc.RetryMax,
		}
	}

	classifiers := make([]classifier.Classifier, 0, len(cfg.Classifiers))
	for _, cc := range cfg.Classifiers {
		c, err := buildClassifier(cc)
		if err != nil {
			return nil, fmt.Errorf("classifier %q: %w", cc.Name, err)
		}
		classifiers = append(classifiers, c)
	}

	routers := make([]router.Router, 0, len(cfg.Routers))
	for _, rc := range cfg.Routers {
		r, err := buildRouter(rc, providers)
		if err != nil {
			return nil, fmt.Errorf("router %q: %w", rc.Name, err)
		}
		routers = append(routers, r)
	}
	if len(routers) == 0 {
		return nil, fmt.Errorf("no routers configured")
	}
	mainRouter := combineRouters(routers)

	preGuardrails := make([]guardrail.Guardrail, 0, len(cfg.Guardrails.Pre))
	for _, gc := range cfg.Guardrails.Pre {
		g, err := buildGuardrail(gc)
		if err != nil {
			return nil, fmt.Errorf("guardrail %q: %w", gc.Name, err)
		}
		preGuardrails = append(preGuardrails, g)
	}
	postGuardrails := make([]guardrail.Guardrail, 0, len(cfg.Guardrails.Post))
	for _, gc := range cfg.Guardrails.Post {
		g, err := buildGuardrail(gc)
		if err != nil {
			return nil, fmt.Errorf("guardrail %q: %w", gc.Name, err)
		}
		postGuardrails = append(postGuardrails, g)
	}

	t := translator.NewDefaultTranslator()
	u := upstream.NewHTTPClient(t)

	return pipeline.NewPipeline(t, t, t, classifiers, mainRouter, u, preGuardrails, postGuardrails, logger), nil
}

func combineRouters(routers []router.Router) router.Router {
	if len(routers) == 1 {
		return routers[0]
	}
	return router.NewChainedRouter("chained", routers)
}

func configuredModels(cfg *config.Config) []arbiterhttp.Model {
	models := make([]arbiterhttp.Model, 0)
	for provider, providerConfig := range cfg.Providers {
		for _, model := range providerConfig.Models {
			models = append(models, arbiterhttp.Model{ID: model, Provider: provider})
		}
	}
	slices.SortFunc(models, func(a, b arbiterhttp.Model) int {
		if a.ID != b.ID {
			return cmp.Compare(a.ID, b.ID)
		}
		return cmp.Compare(a.Provider, b.Provider)
	})
	return models
}

func buildClassifier(cc config.ClassifierConfig) (classifier.Classifier, error) {
	switch cc.Type {
	case "heuristic", "capability_detector":
		keywords, err := stringListMap(cc.Config, "keywords", "detectors")
		if err != nil {
			return nil, err
		}
		return classifier.NewHeuristicClassifier(cc.Name, keywords), nil
	default:
		return nil, fmt.Errorf("unknown classifier type %q", cc.Type)
	}
}

func buildRouter(rc config.RouterConfig, providers map[string]types.ProviderConfig) (router.Router, error) {
	switch rc.Type {
	case "simple":
		defaultProvider, _ := rc.Config["default_provider"].(string)
		fallbackProvider, _ := rc.Config["fallback_provider"].(string)
		if defaultProvider == "" {
			return nil, fmt.Errorf("missing default_provider")
		}
		return router.NewSimpleRouter(rc.Name, defaultProvider, fallbackProvider, providers), nil
	case "policy":
		rules, err := policyRules(rc.Config)
		if err != nil {
			return nil, err
		}
		return router.NewPolicyRouter(rc.Name, rules, providers), nil
	default:
		return nil, fmt.Errorf("unknown router type %q", rc.Type)
	}
}

// policyRules parses the "rules" list out of a policy router's config block.
// Each rule's "when" clause is optional per-field (a missing field is a
// wildcard); see router.PolicyCondition.
func policyRules(cfg map[string]interface{}) ([]router.PolicyRule, error) {
	raw, _ := cfg["rules"].([]interface{})
	rules := make([]router.PolicyRule, 0, len(raw))
	for i, item := range raw {
		m, ok := item.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("rule %d: expected a map", i)
		}
		provider, _ := m["provider"].(string)
		if provider == "" {
			return nil, fmt.Errorf("rule %d: missing provider", i)
		}
		model, _ := m["model"].(string)

		var when router.PolicyCondition
		if w, ok := m["when"].(map[string]interface{}); ok {
			when.Intent, _ = w["intent"].(string)
			when.CostSensitivity, _ = w["cost_sensitivity"].(string)
			if caps, ok := w["capabilities"].([]interface{}); ok {
				for _, c := range caps {
					if s, ok := c.(string); ok {
						when.Capabilities = append(when.Capabilities, s)
					}
				}
			}
		}

		rules = append(rules, router.PolicyRule{When: when, Provider: provider, Model: model})
	}
	return rules, nil
}

func buildGuardrail(gc config.GuardrailConfig) (guardrail.Guardrail, error) {
	switch gc.Type {
	case "system_prompt":
		prompt, _ := gc.Config["prompt"].(string)
		override, _ := gc.Config["override"].(bool)
		return guardrail.NewSystemPromptGuardrail(gc.Name, prompt, override), nil
	case "rate_limit":
		perMinute := intFromConfig(gc.Config, "per_minute")
		perDay := intFromConfig(gc.Config, "per_day")
		return guardrail.NewRateLimitGuardrail(gc.Name, perMinute, perDay), nil
	default:
		return nil, fmt.Errorf("unknown guardrail type %q", gc.Type)
	}
}

// stringListMap pulls a map[string][]string out of a classifier config
// block, trying each of the given keys in turn (lanes.yaml uses "keywords"
// for the domain classifier and "detectors" for the capability classifier —
// same shape, different name).
func stringListMap(cfg map[string]interface{}, keys ...string) (map[string][]string, error) {
	var raw map[string]interface{}
	for _, k := range keys {
		if v, ok := cfg[k]; ok {
			raw, _ = v.(map[string]interface{})
			break
		}
	}
	if raw == nil {
		return nil, fmt.Errorf("missing one of %v in config", keys)
	}

	out := make(map[string][]string, len(raw))
	for intent, v := range raw {
		list, ok := v.([]interface{})
		if !ok {
			continue
		}
		words := make([]string, 0, len(list))
		for _, item := range list {
			if s, ok := item.(string); ok {
				words = append(words, s)
			}
		}
		out[intent] = words
	}
	return out, nil
}

func intFromConfig(cfg map[string]interface{}, key string) int {
	v, ok := cfg[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	default:
		return 0
	}
}
