package upstream

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cnf/arbiter/pkg/types"
)

// decisionServer stands in for the decisions endpoint. It records the request
// it received so a test can assert the wire shape, and replies with whatever
// body/status the case set.
type decisionServer struct {
	*httptest.Server
	lastBody   []byte
	lastHeader http.Header
	lastPath   string
}

func newDecisionServer(t *testing.T, status int, body string) *decisionServer {
	t.Helper()
	ds := &decisionServer{}
	ds.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ds.lastBody, _ = io.ReadAll(r.Body)
		ds.lastHeader = r.Header.Clone()
		ds.lastPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(ds.Close)
	return ds
}

func decisionRoute(url string) types.Route {
	return types.Route{
		Provider: "or-decisions",
		Model:    "~typesafe/jev-latest",
		Config: types.ProviderConfig{
			Name: "or-decisions", Type: "decisions",
			Endpoint: url, APIKey: "test-key",
			Headers: map[string]string{"X-Custom": "yes"},
		},
	}
}

func sampleDecisionRequest() *types.DecisionRequest {
	return &types.DecisionRequest{
		State: "please refactor this",
		Questions: map[string]types.DecisionQuestion{
			"domain": {
				Type:         types.DecisionChoice,
				Instructions: "Pick the category.",
				Criteria: map[string]string{
					"code_generation": "wants code written",
					"other":           "none of the above apply",
				},
			},
		},
	}
}

// The endpoint is called verbatim, with no path appended: for a "decisions"
// provider `endpoint` is the COMPLETE URL. Every other provider type appends
// its own suffix, so this is the one assertion that pins the deliberate
// inconsistency in place — and it is what lets a vendor path change be a config
// edit rather than a code change.
func TestDecideCallsEndpointVerbatim(t *testing.T) {
	const body = `{"model":"jev-1.13.0","answers":{"domain":{"type":"choice","choice":"code_generation","confidence":0.92,"probabilities":{"code_generation":0.92,"other":0.08}}},"usage":{"input_tokens":190,"output_tokens":0}}`
	srv := newDecisionServer(t, 200, body)
	// A path with no /chat/completions in it, so an appended suffix would show.
	url := srv.URL + "/api/alpha/decisions"
	client := NewHTTPClient(nil)

	resp, err := client.Decide(context.Background(), decisionRoute(url), sampleDecisionRequest())
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if srv.lastPath != "/api/alpha/decisions" {
		t.Fatalf("path = %q, want /api/alpha/decisions with nothing appended", srv.lastPath)
	}
	if resp.Model != "jev-1.13.0" {
		t.Fatalf("Model = %q, want the concrete version the endpoint resolved to", resp.Model)
	}
	if got := resp.Answers["domain"].Choice; got != "code_generation" {
		t.Fatalf("choice = %q, want code_generation", got)
	}
	if got := resp.Answers["domain"].Confidence; got != 0.92 {
		t.Fatalf("confidence = %v, want 0.92", got)
	}
	if got := resp.Answers["domain"].Probabilities["other"]; got != 0.08 {
		t.Fatalf("distribution lost: other = %v, want 0.08", got)
	}
	if resp.Usage.InputTokens != 190 {
		t.Fatalf("usage lost: input_tokens = %d, want 190", resp.Usage.InputTokens)
	}
	// Raw must carry the body: it is what a stored call record keeps, and the
	// only thing that debugs a wrong verdict when the typed fields disagree.
	if !strings.Contains(resp.Raw, "jev-1.13.0") {
		t.Fatalf("Raw did not carry the response body: %q", resp.Raw)
	}
}

// The wire shape is state + questions, not messages, and auth is the bearer
// convention with the provider's own headers applied last.
func TestDecideSendsStateAndQuestions(t *testing.T) {
	srv := newDecisionServer(t, 200, `{"model":"m","answers":{"domain":{"type":"choice","choice":"other"}}}`)
	client := NewHTTPClient(nil)

	if _, err := client.Decide(context.Background(), decisionRoute(srv.URL+"/d"), sampleDecisionRequest()); err != nil {
		t.Fatalf("Decide: %v", err)
	}

	var sent map[string]interface{}
	if err := json.Unmarshal(srv.lastBody, &sent); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	if _, ok := sent["messages"]; ok {
		t.Fatal("body carries `messages` — this must not masquerade as a chat completion")
	}
	if sent["state"] != "please refactor this" {
		t.Fatalf("state = %v, want the classified text", sent["state"])
	}
	if sent["model"] != "~typesafe/jev-latest" {
		t.Fatalf("model = %v, want the route's model", sent["model"])
	}
	questions, ok := sent["questions"].(map[string]interface{})
	if !ok || len(questions) != 1 {
		t.Fatalf("questions = %v, want one entry", sent["questions"])
	}
	if srv.lastHeader.Get("Authorization") != "Bearer test-key" {
		t.Fatalf("Authorization = %q, want Bearer test-key", srv.lastHeader.Get("Authorization"))
	}
	if srv.lastHeader.Get("X-Custom") != "yes" {
		t.Fatalf("provider headers were not applied: %v", srv.lastHeader)
	}
}

// A 429 must carry its Retry-After the same way a chat call's does, so the
// fallback/cooldown path is identical — a decision call must not be the one
// upstream call that ignores a cooldown.
func TestDecidePropagatesRetryAfterOn429(t *testing.T) {
	srv := newDecisionServer(t, 200, `{}`)
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	})
	client := NewHTTPClient(nil)

	_, err := client.Decide(context.Background(), decisionRoute(srv.URL+"/d"), sampleDecisionRequest())
	if err == nil {
		t.Fatal("want an error on 429")
	}
	ue, ok := err.(interface{ Error() string })
	if !ok {
		t.Fatalf("unexpected error type %T", err)
	}
	if !strings.Contains(ue.Error(), "rate limited") {
		t.Fatalf("error lost the upstream body: %v", err)
	}
}

// A 200 with no answers is not a usable verdict, and an absent answer is
// indistinguishable from an answered zero downstream — so it must fail here,
// where the body is still in hand, rather than be acted on as "nothing matched".
func TestDecideRejects200WithNoAnswers(t *testing.T) {
	srv := newDecisionServer(t, 200, `{"model":"m","usage":{"input_tokens":10}}`)
	client := NewHTTPClient(nil)

	_, err := client.Decide(context.Background(), decisionRoute(srv.URL+"/d"), sampleDecisionRequest())
	if err == nil || !strings.Contains(err.Error(), "carried no answers") {
		t.Fatalf("want a no-answers error, got %v", err)
	}
}

// A transport failure is a 502 rather than a zero, matching the rule the
// pipeline applies to real client traffic.
func TestDecideTransportFailure(t *testing.T) {
	srv := newDecisionServer(t, 200, `{}`)
	url := srv.URL + "/d"
	srv.Close() // nothing listening

	client := NewHTTPClient(nil)
	_, err := client.Decide(context.Background(), decisionRoute(url), sampleDecisionRequest())
	if err == nil {
		t.Fatal("want an error when the endpoint is unreachable")
	}
}

// The provider's own timeout bounds the call, like every other provider type.
func TestDecideHonoursProviderTimeout(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(200)
	}))
	defer slow.Close()

	route := decisionRoute(slow.URL + "/d")
	route.Config.Timeout = 20 * time.Millisecond

	client := NewHTTPClient(nil)
	if _, err := client.Decide(context.Background(), route, sampleDecisionRequest()); err == nil {
		t.Fatal("want a timeout error")
	}
}
