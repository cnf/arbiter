package upstream

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cnf/arbiter/internal/translator"
	"github.com/cnf/arbiter/pkg/types"
)

// sseServer serves an OpenAI-style SSE stream: chunks events with `gap`
// between them, then finishes. Flushes each event so the client sees it
// as it is written rather than at body close.
func sseServer(t *testing.T, chunks int, gap time.Duration) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Errorf("test server response does not support flushing")
			return
		}
		for i := 0; i < chunks; i++ {
			time.Sleep(gap)
			_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", "tok")
			flusher.Flush()
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
}

func streamRoute(endpoint string, timeout time.Duration) types.Route {
	return types.Route{
		Provider: "test",
		Model:    "m",
		Config: types.ProviderConfig{
			Name:     "test",
			Type:     "openai",
			Endpoint: endpoint,
			Timeout:  timeout,
		},
	}
}

// A stream that keeps producing must not be cut just because its total
// duration exceeds the provider timeout — the timeout bounds silence, not
// length. Before the watchdog, the provider timeout was a hard total
// deadline and this stream was truncated mid-flight.
func TestStreamOutlivesProviderTimeoutWhileProducing(t *testing.T) {
	// 6 events, 40ms apart = ~240ms total, against a 100ms timeout.
	srv := sseServer(t, 6, 40*time.Millisecond)
	defer srv.Close()

	c := NewHTTPClient(translator.NewDefaultTranslator())
	ch, errCh, err := c.SendStream(context.Background(), streamRoute(srv.URL, 100*time.Millisecond), &types.NormalizedRequest{Stream: true})
	if err != nil {
		t.Fatalf("SendStream: %v", err)
	}

	got := 0
	for range ch {
		got++
	}
	if got != 6 {
		t.Fatalf("received %d events, want all 6 — stream was truncated by the provider timeout", got)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("stream ended with error: %v", err)
	}
}

// The flip side: an upstream that goes silent past the idle timeout gets
// cancelled rather than hanging forever. The channel must close.
func TestStreamCutWhenUpstreamGoesSilent(t *testing.T) {
	// First event lands quickly, then a long gap the watchdog should trip on.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"tok\"}}]}\n\n")
		flusher.Flush()
		time.Sleep(2 * time.Second) // silence, far past the idle timeout
	}))
	defer srv.Close()

	c := NewHTTPClient(translator.NewDefaultTranslator())
	ch, errCh, err := c.SendStream(context.Background(), streamRoute(srv.URL, 100*time.Millisecond), &types.NormalizedRequest{Stream: true})
	if err != nil {
		t.Fatalf("SendStream: %v", err)
	}

	done := make(chan int, 1)
	go func() {
		n := 0
		for range ch {
			n++
		}
		done <- n
	}()

	select {
	case n := <-done:
		if n != 1 {
			t.Fatalf("received %d events, want the 1 sent before the silence", n)
		}
		if err := <-errCh; err == nil {
			t.Fatal("stream cut by the watchdog reported a clean finish, want a non-nil error")
		}
	case <-time.After(time.Second):
		t.Fatal("stream did not close after the upstream went silent — watchdog never fired")
	}
}

// A provider timeout of 0 means no watchdog at all; the stream should still
// run to completion and close normally.
func TestStreamWithoutTimeoutRunsToCompletion(t *testing.T) {
	srv := sseServer(t, 3, 10*time.Millisecond)
	defer srv.Close()

	c := NewHTTPClient(translator.NewDefaultTranslator())
	ch, errCh, err := c.SendStream(context.Background(), streamRoute(srv.URL, 0), &types.NormalizedRequest{Stream: true})
	if err != nil {
		t.Fatalf("SendStream: %v", err)
	}

	got := 0
	for range ch {
		got++
	}
	if err := <-errCh; err != nil {
		t.Fatalf("stream ended with error: %v", err)
	}
	if got != 3 {
		t.Fatalf("received %d events, want 3", got)
	}
}
