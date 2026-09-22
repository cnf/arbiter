package http

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	arbitererrors "github.com/cnf/arbiter/pkg/errors"
)

// The client-visible half of the transport-failure rule: an upstream error with
// no usable status (a dial failure records 0) must become 502, not 0 and not a
// passthrough of a bogus code. The pipeline records the same value in the event
// store (see pipeline.upstreamFailureStatus), so this keeps the two halves
// agreeing — the store's error counts and the client's status describe one
// event.
func TestWriteArbiterErrorMapsTransportFailureToBadGateway(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"a dial failure carries no status", arbitererrors.NewUpstreamError("p", 0, "connection refused", nil), http.StatusBadGateway},
		{"an out-of-range status becomes 502", arbitererrors.NewUpstreamError("p", 700, "weird", nil), http.StatusBadGateway},
		{"an upstream's own status passes through", arbitererrors.NewUpstreamError("p", 429, "rate limited", nil), 429},
		{"a guardrail keeps its status", arbitererrors.NewGuardrailError("g", 429, nil), 429},
		{"a stop rule keeps its configured status", arbitererrors.NewStopError(406, "not like that"), 406},
		{"an unknown error is 500", fmt.Errorf("boom"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		writeArbiterError(rec, tc.err)
		if rec.Code != tc.want {
			t.Errorf("%s: status = %d, want %d", tc.name, rec.Code, tc.want)
		}
	}
}

// A stop rule's message must reach the client body verbatim — it is the
// operator's own explanation for the refusal (#43's example:
// "not like that poopoohead"), and a generic wrapper message here would
// defeat the point of letting the operator write one at all.
func TestWriteArbiterErrorStopCarriesMessageToBody(t *testing.T) {
	rec := httptest.NewRecorder()
	writeArbiterError(rec, arbitererrors.NewStopError(406, "not like that poopoohead"))
	if rec.Code != 406 {
		t.Fatalf("status = %d, want 406", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "not like that poopoohead") {
		t.Errorf("body = %q, want it to contain the stop rule's message", body)
	}
}
