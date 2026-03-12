package stream_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/laenen-partners/dsx"
	"github.com/laenen-partners/dsx/pubsub/chanpubsub"
	"github.com/laenen-partners/dsx/stream"
	"github.com/starfederation/datastar-go/datastar"
)

// newPubSub creates an in-process pub/sub for testing.
func newPubSub(t *testing.T) *chanpubsub.ChanPubSub {
	t.Helper()
	ps := chanpubsub.New()
	t.Cleanup(func() {
		if err := ps.Close(); err != nil {
			t.Errorf("closing pubsub: %v", err)
		}
	})
	return ps
}

func TestScopeKey(t *testing.T) {
	tests := []struct {
		scope string
		want  string
	}{
		{"invoice:42", "invoice_42"},
		{"invoices:*", "invoices_WILD"},
		{"workspace:1:*", "workspace_1_WILD"},
		{"simple", "simple"},
		{"a.b:c", "a_b_c"},
	}
	for _, tt := range tests {
		t.Run(tt.scope, func(t *testing.T) {
			got := stream.ScopeKey(tt.scope)
			if got != tt.want {
				t.Errorf("ScopeKey(%q) = %q, want %q", tt.scope, got, tt.want)
			}
		})
	}
}

func TestScopeSignals(t *testing.T) {
	got := stream.ScopeSignals("counter:shared")
	// Should produce valid Datastar signals object syntax
	if !strings.HasPrefix(got, "{") || !strings.HasSuffix(got, "}") {
		t.Errorf("ScopeSignals should be wrapped in {}, got: %s", got)
	}
	if !strings.Contains(got, "_stream") {
		t.Errorf("ScopeSignals should contain _stream namespace, got: %s", got)
	}
	if !strings.Contains(got, "counter_shared") {
		t.Errorf("ScopeSignals should contain counter_shared key, got: %s", got)
	}
	t.Logf("ScopeSignals output: %s", got)
}

func TestWatchEffect(t *testing.T) {
	wctx := &dsx.Context{}
	ctx := wctx.WithContext(context.Background())

	effect := stream.WatchEffect(ctx, "counter:shared", "/api/counter")

	// Should register a watcher
	if len(wctx.Watchers) != 1 || wctx.Watchers[0].Scope != "counter:shared" {
		t.Errorf("WatchEffect should register watcher, got: %v", wctx.Watchers)
	}

	// Should return a data-effect expression
	if !strings.Contains(effect, "$_stream.counter_shared") {
		t.Errorf("effect should reference $_stream.counter_shared, got: %s", effect)
	}
	if !strings.Contains(effect, "@get('/api/counter')") {
		t.Errorf("effect should contain @get with reload URL, got: %s", effect)
	}
	t.Logf("WatchEffect output: %s", effect)
}

func TestWatchEffect_UniqueKeys(t *testing.T) {
	wctx := &dsx.Context{}
	ctx := wctx.WithContext(context.Background())

	effect1 := stream.WatchEffect(ctx, "counter:shared", "/api/counter")
	effect2 := stream.WatchEffect(ctx, "counter:shared", "/api/count")

	// Should register two watchers with unique keys
	if len(wctx.Watchers) != 2 {
		t.Fatalf("expected 2 watchers, got %d", len(wctx.Watchers))
	}
	if wctx.Watchers[0].Key == wctx.Watchers[1].Key {
		t.Errorf("watchers should have unique keys, both got: %s", wctx.Watchers[0].Key)
	}
	// Effects should reference different signals
	if effect1 == effect2 {
		t.Error("effects for same scope should have different signal keys")
	}
	t.Logf("Key 1: %s, Key 2: %s", wctx.Watchers[0].Key, wctx.Watchers[1].Key)
}

func TestBrokerInvalidate(t *testing.T) {
	ps := newPubSub(t)
	broker := stream.NewBroker(ps)

	// Subscribe to the expected subject
	received := make(chan []byte, 1)
	_, err := ps.Subscribe("dsx.scope.counter.shared", func(data []byte) {
		received <- data
	})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	// Invalidate
	if err := broker.Invalidate("counter:shared"); err != nil {
		t.Fatalf("Invalidate failed: %v", err)
	}

	select {
	case <-received:
		// ok
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive message within 2s")
	}
}

func TestBrokerInvalidate_CustomPrefix(t *testing.T) {
	ps := newPubSub(t)
	broker := stream.NewBroker(ps, stream.WithSubjectPrefix("myapp.scope"))

	received := make(chan []byte, 1)
	_, err := ps.Subscribe("myapp.scope.counter.shared", func(data []byte) {
		received <- data
	})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	if err := broker.Invalidate("counter:shared"); err != nil {
		t.Fatalf("Invalidate failed: %v", err)
	}

	select {
	case <-received:
		// ok
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive message")
	}
}

func TestBrokerInvalidateMany(t *testing.T) {
	ps := newPubSub(t)
	broker := stream.NewBroker(ps)

	received := make(chan string, 10)
	_, err := ps.Subscribe("dsx.scope.>", func(data []byte) {
		received <- "got"
	})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	if err := broker.InvalidateMany("counter:shared", "invoice:42"); err != nil {
		t.Fatalf("InvalidateMany failed: %v", err)
	}

	for range 2 {
		select {
		case <-received:
		case <-time.After(2 * time.Second):
			t.Fatal("timeout waiting for messages")
		}
	}
}

// parseSSEEvents reads SSE events from a reader and returns them.
func parseSSEEvents(r io.Reader) []map[string]string {
	var events []map[string]string
	scanner := bufio.NewScanner(r)
	current := map[string]string{}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if len(current) > 0 {
				events = append(events, current)
				current = map[string]string{}
			}
			continue
		}
		if strings.HasPrefix(line, "event: ") {
			current["event"] = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			current["data"] += strings.TrimPrefix(line, "data: ") + "\n"
		}
	}
	if len(current) > 0 {
		events = append(events, current)
	}
	return events
}

func TestStreamHandler_NoScopes(t *testing.T) {
	ps := newPubSub(t)
	broker := stream.NewBroker(ps)

	req := httptest.NewRequest("GET", "/stream", nil)
	w := httptest.NewRecorder()

	broker.Handler().ServeHTTP(w, req)

	// Should return immediately with no SSE when no scopes
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestStreamHandler_ReceivesInvalidation(t *testing.T) {
	ps := newPubSub(t)
	broker := stream.NewBroker(ps)

	// Create a cancellable context for the SSE request
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequest("GET", "/stream?scope=counter:shared", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	// Run handler in background
	done := make(chan struct{})
	go func() {
		defer close(done)
		broker.Handler().ServeHTTP(w, req)
	}()

	// Give the handler time to subscribe
	time.Sleep(100 * time.Millisecond)

	// Invalidate the scope
	if err := broker.Invalidate("counter:shared"); err != nil {
		t.Fatalf("Invalidate failed: %v", err)
	}

	// Give time for the SSE event to be written
	time.Sleep(200 * time.Millisecond)

	// Cancel context to stop the handler
	cancel()
	<-done

	body := w.Body.String()
	t.Logf("SSE response body:\n%s", body)

	// Check SSE headers
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/event-stream") {
		t.Errorf("expected text/event-stream content type, got: %s", ct)
	}

	// Parse SSE events
	events := parseSSEEvents(strings.NewReader(body))
	if len(events) == 0 {
		t.Fatal("expected at least one SSE event, got none")
	}

	// Should be a patch-signals event with _stream.counter_shared = true
	found := false
	for _, evt := range events {
		t.Logf("Event: %v", evt)
		if evt["event"] == "datastar-patch-signals" {
			if strings.Contains(evt["data"], "counter_shared") {
				found = true
			}
		}
	}
	if !found {
		t.Error("did not find datastar-patch-signals event with counter_shared")
	}
}

func TestStreamHandler_WildcardScope(t *testing.T) {
	ps := newPubSub(t)
	broker := stream.NewBroker(ps)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Subscribe to wildcard scope "invoices:*"
	req := httptest.NewRequest("GET", "/stream?scope=invoices:*", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		broker.Handler().ServeHTTP(w, req)
	}()

	time.Sleep(100 * time.Millisecond)

	// Invalidate a specific invoice — should be received by wildcard subscriber
	if err := broker.Invalidate("invoices:42"); err != nil {
		t.Fatalf("Invalidate failed: %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

	body := w.Body.String()
	t.Logf("SSE response body:\n%s", body)

	events := parseSSEEvents(strings.NewReader(body))
	found := false
	for _, evt := range events {
		if evt["event"] == "datastar-patch-signals" && strings.Contains(evt["data"], "invoices_WILD") {
			found = true
		}
	}
	if !found {
		t.Error("wildcard scope did not receive invalidation for invoices:42")
	}
}

func TestCounterHandler_GetCounter(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/stream/counter", nil)
	w := httptest.NewRecorder()

	// Simulate the counter handler directly
	var counter atomic.Int64
	counter.Store(42)

	handler := func(w http.ResponseWriter, r *http.Request) {
		sse := datastar.NewSSE(w, r)
		count := counter.Load()
		_ = sse.PatchElements(
			fmt.Sprintf(`<span id="stream-counter-value" class="text-6xl font-bold tabular-nums">%d</span>`, count),
		)
	}

	handler(w, req)

	body := w.Body.String()
	t.Logf("Counter SSE response:\n%s", body)

	// Should contain the counter value
	if !strings.Contains(body, "42") {
		t.Error("response should contain counter value 42")
	}

	// Should be a patch-elements event
	events := parseSSEEvents(strings.NewReader(body))
	if len(events) == 0 {
		t.Fatal("expected at least one SSE event")
	}

	found := false
	for _, evt := range events {
		if evt["event"] == "datastar-patch-elements" {
			if strings.Contains(evt["data"], "stream-counter-value") {
				found = true
			}
		}
	}
	if !found {
		t.Error("expected patch-elements event targeting stream-counter-value")
	}
}

func TestConnectTemplate_RendersWhenScopesExist(t *testing.T) {
	wctx := &dsx.Context{
		StreamURL: "/showcase/stream",
		Watchers:  []dsx.Watcher{{Scope: "counter:shared", Key: "counter_shared"}},
	}
	ctx := wctx.WithContext(context.Background())

	var buf bytes.Buffer
	err := stream.Connect().Render(ctx, &buf)
	if err != nil {
		t.Fatalf("Connect render failed: %v", err)
	}

	html := buf.String()
	t.Logf("Connect HTML output:\n%s", html)

	// Should render a hidden div
	if !strings.Contains(html, `style="display:none"`) {
		t.Error("Connect should render a hidden div")
	}

	// Should have data-signals with outer braces
	if !strings.Contains(html, "data-signals=") {
		t.Error("Connect should have data-signals attribute")
	}

	// Check the data-signals value is properly formatted
	if !strings.Contains(html, "_stream") {
		t.Error("data-signals should contain _stream namespace")
	}

	// Should have data-init with the stream URL
	if !strings.Contains(html, "data-init") {
		t.Error("Connect should have data-init attribute")
	}
	if !strings.Contains(html, "/showcase/stream?counter=shared") {
		t.Error("Connect should have stream URL with grouped scope param")
	}

	// Should have requestCancellation disabled
	if !strings.Contains(html, "requestCancellation") {
		t.Error("Connect should have requestCancellation option")
	}
}

func TestConnectTemplate_NoRenderWithoutScopes(t *testing.T) {
	wctx := &dsx.Context{
		StreamURL: "/showcase/stream",
		Watchers:  nil,
	}
	ctx := wctx.WithContext(context.Background())

	var buf bytes.Buffer
	err := stream.Connect().Render(ctx, &buf)
	if err != nil {
		t.Fatalf("Connect render failed: %v", err)
	}

	if buf.Len() > 0 {
		t.Errorf("Connect should render nothing when no scopes, got: %s", buf.String())
	}
}

func TestConnectTemplate_NoRenderWithoutStreamURL(t *testing.T) {
	wctx := &dsx.Context{
		StreamURL: "",
		Watchers:  []dsx.Watcher{{Scope: "counter:shared", Key: "counter_shared"}},
	}
	ctx := wctx.WithContext(context.Background())

	var buf bytes.Buffer
	err := stream.Connect().Render(ctx, &buf)
	if err != nil {
		t.Fatalf("Connect render failed: %v", err)
	}

	if buf.Len() > 0 {
		t.Errorf("Connect should render nothing when no StreamURL, got: %s", buf.String())
	}
}

// TestE2E_FullFlow tests the complete reactive flow:
// 1. Page registers scope via WatchEffect
// 2. Connect template renders with correct signals and stream URL
// 3. Stream handler subscribes and receives invalidation
// 4. Counter handler returns correct SSE response
func TestE2E_FullFlow(t *testing.T) {
	ps := newPubSub(t)
	broker := stream.NewBroker(ps)

	// === Step 1: Simulate page render ===
	wctx := &dsx.Context{
		BasePath:  "/showcase",
		StreamURL: "/showcase/stream",
	}
	ctx := wctx.WithContext(context.Background())

	// Component registers scope (like the stream page does)
	effect := stream.WatchEffect(ctx, "counter:shared", "/showcase/api/stream/counter")
	t.Logf("Step 1 - WatchEffect returned: %s", effect)
	t.Logf("Step 1 - Watchers registered: %v", wctx.Watchers)

	if len(wctx.Watchers) == 0 {
		t.Fatal("no watchers registered after WatchEffect")
	}

	// === Step 2: Render Connect template ===
	var connectBuf bytes.Buffer
	if err := stream.Connect().Render(ctx, &connectBuf); err != nil {
		t.Fatalf("Connect render: %v", err)
	}
	connectHTML := connectBuf.String()
	t.Logf("Step 2 - Connect HTML:\n%s", connectHTML)

	if connectHTML == "" {
		t.Fatal("Connect rendered empty — stream won't connect")
	}

	// === Step 3: Stream handler receives invalidation ===
	streamCtx, streamCancel := context.WithCancel(context.Background())
	defer streamCancel()

	streamReq := httptest.NewRequest("GET", "/showcase/stream?scope=counter:shared", nil).WithContext(streamCtx)
	streamW := httptest.NewRecorder()

	streamDone := make(chan struct{})
	go func() {
		defer close(streamDone)
		broker.Handler().ServeHTTP(streamW, streamReq)
	}()

	// Wait for subscription to be set up
	time.Sleep(150 * time.Millisecond)

	// Simulate what a button click handler does: invalidate
	if err := broker.Invalidate("counter:shared"); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}

	// Wait for SSE event to be written
	time.Sleep(200 * time.Millisecond)

	streamCancel()
	<-streamDone

	streamBody := streamW.Body.String()
	t.Logf("Step 3 - Stream SSE response:\n%s", streamBody)

	streamEvents := parseSSEEvents(strings.NewReader(streamBody))
	if len(streamEvents) == 0 {
		t.Fatal("stream handler produced no SSE events after invalidation")
	}

	// Verify the stale signal was pushed
	staleSignalFound := false
	for _, evt := range streamEvents {
		if evt["event"] == "datastar-patch-signals" {
			data := evt["data"]
			t.Logf("Step 3 - patch-signals data: %s", data)
			if strings.Contains(data, "counter_shared") && strings.Contains(data, "_stream") {
				staleSignalFound = true
			}
		}
	}
	if !staleSignalFound {
		t.Fatal("stream did not push stale signal for counter_shared")
	}

	// === Step 4: Counter handler returns correct response ===
	counterReq := httptest.NewRequest("GET", "/showcase/api/stream/counter", nil)
	counterW := httptest.NewRecorder()

	// Simulate counter handler
	sse := datastar.NewSSE(counterW, counterReq)
	_ = sse.PatchElements(`<span id="stream-counter-value" class="text-6xl font-bold tabular-nums">0</span>`)

	counterBody := counterW.Body.String()
	t.Logf("Step 4 - Counter SSE response:\n%s", counterBody)

	counterEvents := parseSSEEvents(strings.NewReader(counterBody))
	patchFound := false
	for _, evt := range counterEvents {
		if evt["event"] == "datastar-patch-elements" {
			data := evt["data"]
			if strings.Contains(data, "stream-counter-value") && strings.Contains(data, "0") {
				patchFound = true
			}
		}
	}
	if !patchFound {
		t.Fatal("counter handler did not return expected patch-elements event")
	}

	t.Log("=== E2E flow passed: scope registration → Connect render → stream invalidation → counter patch ===")
}

// TestE2E_MutationHandler_NoEmptyPatch verifies mutation handlers
// don't send PatchElements with no target (which causes the browser error).
func TestE2E_MutationHandler_NoEmptyPatch(t *testing.T) {
	ps := newPubSub(t)
	broker := stream.NewBroker(ps)

	var counter atomic.Int64

	// Simulate increment handler
	handler := func(w http.ResponseWriter, r *http.Request) {
		counter.Add(1)
		if err := broker.Invalidate("counter:shared"); err != nil {
			http.Error(w, fmt.Sprintf("Invalidate: %v", err), http.StatusInternalServerError)
			return
		}
		datastar.NewSSE(w, r)
	}

	req := httptest.NewRequest("GET", "/api/stream/increment", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	body := w.Body.String()
	t.Logf("Mutation handler response:\n%q", body)

	// Should NOT contain patch-elements (which would error with no target)
	if strings.Contains(body, "datastar-patch-elements") {
		t.Error("mutation handler should NOT send patch-elements event")
	}

	// Counter should have been incremented
	if counter.Load() != 1 {
		t.Errorf("counter should be 1, got %d", counter.Load())
	}
}

// TestE2E_DataSignalsFormat verifies the exact format of data-signals
// that Datastar expects.
func TestE2E_DataSignalsFormat(t *testing.T) {
	// Test ScopeSignals format
	signals := stream.ScopeSignals("counter:shared")
	t.Logf("ScopeSignals: %s", signals)

	// Must start with { and end with }
	if signals[0] != '{' || signals[len(signals)-1] != '}' {
		t.Errorf("ScopeSignals must be wrapped in {}, got: %s", signals)
	}

	// Must contain the namespace
	if !strings.Contains(signals, "_stream") {
		t.Errorf("must contain _stream, got: %s", signals)
	}

	// The format should be: {_stream: {"counter_shared":false}}
	expected := `{_stream: {"counter_shared":false}}`
	if signals != expected {
		t.Errorf("ScopeSignals format mismatch\n  got:  %s\n  want: %s", signals, expected)
	}

	// Test Connect template produces the same format
	wctx := &dsx.Context{
		StreamURL: "/stream",
		Watchers:  []dsx.Watcher{{Scope: "counter:shared", Key: "counter_shared"}},
	}
	ctx := wctx.WithContext(context.Background())

	var buf bytes.Buffer
	if err := stream.Connect().Render(ctx, &buf); err != nil {
		t.Fatalf("Connect render: %v", err)
	}
	html := buf.String()
	t.Logf("Connect HTML: %s", html)

	if !strings.Contains(html, "_stream") {
		t.Error("Connect HTML should contain _stream")
	}
}

// TestE2E_MultipleScopes tests with multiple scopes registered.
func TestE2E_MultipleScopes(t *testing.T) {
	ps := newPubSub(t)
	broker := stream.NewBroker(ps)

	wctx := &dsx.Context{
		StreamURL: "/stream",
	}
	ctx := wctx.WithContext(context.Background())

	stream.WatchEffect(ctx, "counter:shared", "/api/counter")
	stream.WatchEffect(ctx, "invoice:42", "/api/invoice/42")

	if len(wctx.Watchers) != 2 {
		t.Fatalf("expected 2 watchers, got %d", len(wctx.Watchers))
	}

	// Render Connect
	var buf bytes.Buffer
	if err := stream.Connect().Render(ctx, &buf); err != nil {
		t.Fatalf("Connect render: %v", err)
	}
	html := buf.String()
	t.Logf("Connect HTML with 2 scopes:\n%s", html)

	// Should use grouped scope format (counter=shared&invoice=42)
	if !strings.Contains(html, "counter=shared") || !strings.Contains(html, "invoice=42") {
		t.Error("missing grouped scope params")
	}

	// Test stream handler with multiple scopes
	streamCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequest("GET", "/stream?scope=counter:shared&scope=invoice:42", nil).WithContext(streamCtx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		broker.Handler().ServeHTTP(w, req)
	}()

	time.Sleep(150 * time.Millisecond)

	// Invalidate only invoice:42
	if err := broker.Invalidate("invoice:42"); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	cancel()
	<-done

	body := w.Body.String()
	t.Logf("Stream body (invoice:42 invalidation):\n%s", body)

	events := parseSSEEvents(strings.NewReader(body))
	invoiceStale := false
	for _, evt := range events {
		if evt["event"] == "datastar-patch-signals" && strings.Contains(evt["data"], "invoice_42") {
			invoiceStale = true
		}
	}
	if !invoiceStale {
		t.Error("should have received stale signal for invoice_42")
	}
}

// TestMultiWatcher_SameScopeBothReceive verifies that two components watching
// the same scope both get their stale signals pushed when invalidation occurs.
func TestMultiWatcher_SameScopeBothReceive(t *testing.T) {
	ps := newPubSub(t)
	broker := stream.NewBroker(ps)

	// Simulate two components watching customers:* with unique keys.
	keysJSON := `{"customers:*":["customers_WILD","customers_WILD_2"]}`

	streamCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequest("GET",
		"/stream?customers=*&keys="+url.QueryEscape(keysJSON), nil).WithContext(streamCtx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		broker.Handler().ServeHTTP(w, req)
	}()

	time.Sleep(150 * time.Millisecond)
	if err := broker.Invalidate("customers:42"); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	cancel()
	<-done

	body := w.Body.String()
	t.Logf("SSE body:\n%s", body)

	// Both keys should be present in a single patch-signals event.
	events := parseSSEEvents(strings.NewReader(body))
	found1, found2 := false, false
	for _, evt := range events {
		if evt["event"] == "datastar-patch-signals" {
			if strings.Contains(evt["data"], "customers_WILD") {
				found1 = true
			}
			if strings.Contains(evt["data"], "customers_WILD_2") {
				found2 = true
			}
		}
	}
	if !found1 || !found2 {
		t.Errorf("expected both customers_WILD and customers_WILD_2 in stale signals, got key1=%v key2=%v", found1, found2)
	}
}

func TestParseScopes(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want []string
	}{
		{"single", "/stream?scope=a:1", []string{"a:1"}},
		{"multiple repeated", "/stream?scope=a:1&scope=b:2", []string{"a:1", "b:2"}},
		{"comma-separated", "/stream?scope=a:1,b:2", []string{"a:1", "b:2"}},
		{"mixed", "/stream?scope=a:1,b:2&scope=c:3", []string{"a:1", "b:2", "c:3"}},
		{"empty values trimmed", "/stream?scope=a:1,,b:2", []string{"a:1", "b:2"}},
		{"whitespace trimmed", "/stream?scope=%20a:1%20,%20b:2%20", []string{"a:1", "b:2"}},
		{"no scope param", "/stream", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ps := newPubSub(t)
			broker := stream.NewBroker(ps)

			ctx, cancel := context.WithCancel(context.Background())

			req := httptest.NewRequest("GET", tt.url, nil).WithContext(ctx)
			w := httptest.NewRecorder()

			done := make(chan struct{})
			go func() {
				defer close(done)
				broker.Handler().ServeHTTP(w, req)
			}()

			time.Sleep(100 * time.Millisecond)

			// Invalidate all expected scopes
			for _, scope := range tt.want {
				if err := broker.Invalidate(scope); err != nil {
					t.Fatalf("Invalidate %q: %v", scope, err)
				}
			}

			time.Sleep(200 * time.Millisecond)
			cancel()
			<-done

			body := w.Body.String()
			events := parseSSEEvents(strings.NewReader(body))

			// Verify we received events for each expected scope
			for _, scope := range tt.want {
				key := stream.ScopeKey(scope)
				found := false
				for _, evt := range events {
					if evt["event"] == "datastar-patch-signals" && strings.Contains(evt["data"], key) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected stale event for scope %q (key %q), not found in %d events", scope, key, len(events))
				}
			}
		})
	}
}

func TestBrokerInvalidateWithData(t *testing.T) {
	ps := newPubSub(t)
	broker := stream.NewBroker(ps)

	type payload struct {
		Name  string `json:"name"`
		Value int    `json:"value"`
	}

	received := make(chan []byte, 1)
	_, err := ps.Subscribe("dsx.scope.invoice.42", func(data []byte) {
		received <- data
	})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	data := payload{Name: "test", Value: 99}
	if err := broker.InvalidateWithData("invoice:42", data); err != nil {
		t.Fatalf("InvalidateWithData failed: %v", err)
	}

	select {
	case raw := <-received:
		if len(raw) == 0 {
			t.Fatal("expected non-nil payload")
		}
		var got payload
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}
		if got.Name != "test" || got.Value != 99 {
			t.Errorf("unexpected payload: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive message")
	}
}

func TestStreamHandler_ReceivesPayload(t *testing.T) {
	ps := newPubSub(t)
	broker := stream.NewBroker(ps)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequest("GET", "/stream?scope=invoice:42", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		broker.Handler().ServeHTTP(w, req)
	}()

	time.Sleep(100 * time.Millisecond)

	if err := broker.InvalidateWithData("invoice:42", map[string]any{"total": 100}); err != nil {
		t.Fatalf("InvalidateWithData failed: %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

	body := w.Body.String()
	t.Logf("SSE body:\n%s", body)

	events := parseSSEEvents(strings.NewReader(body))
	foundStale := false
	foundData := false
	for _, evt := range events {
		if evt["event"] == "datastar-patch-signals" {
			data := evt["data"]
			if strings.Contains(data, "_stream") && strings.Contains(data, "invoice_42") {
				foundStale = true
			}
			if strings.Contains(data, "_streamData") && strings.Contains(data, "invoice_42") {
				foundData = true
			}
		}
	}
	if !foundStale {
		t.Error("expected stale signal for invoice_42")
	}
	if !foundData {
		t.Error("expected _streamData payload for invoice_42")
	}
}

func TestStreamHandler_PayloadFallbackToStaleOnly(t *testing.T) {
	ps := newPubSub(t)
	broker := stream.NewBroker(ps)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequest("GET", "/stream?scope=counter:shared", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		broker.Handler().ServeHTTP(w, req)
	}()

	time.Sleep(100 * time.Millisecond)

	// Plain Invalidate (no data)
	if err := broker.Invalidate("counter:shared"); err != nil {
		t.Fatalf("Invalidate failed: %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

	body := w.Body.String()
	events := parseSSEEvents(strings.NewReader(body))

	foundStale := false
	for _, evt := range events {
		if evt["event"] == "datastar-patch-signals" {
			data := evt["data"]
			if strings.Contains(data, "_stream") && strings.Contains(data, "counter_shared") {
				foundStale = true
			}
			// Should NOT contain _streamData for plain invalidation
			if strings.Contains(data, "_streamData") {
				t.Error("plain Invalidate should not include _streamData")
			}
		}
	}
	if !foundStale {
		t.Error("expected stale signal for counter_shared")
	}
}

func TestDynamicScopeRegistration(t *testing.T) {
	ps := newPubSub(t)
	broker := stream.NewBroker(ps)

	// Create a session context
	wctx := &dsx.Context{SessionID: "test-session-123"}
	ctx, cancel := context.WithCancel(wctx.WithContext(context.Background()))
	defer cancel()

	req := httptest.NewRequest("GET", "/stream?scope=counter:shared", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		broker.Handler().ServeHTTP(w, req)
	}()

	time.Sleep(100 * time.Millisecond)

	// Dynamically add a new scope
	if err := broker.AddScope("test-session-123", "invoice:42"); err != nil {
		t.Fatalf("AddScope failed: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	// Now invalidate the dynamically added scope
	if err := broker.Invalidate("invoice:42"); err != nil {
		t.Fatalf("Invalidate failed: %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

	body := w.Body.String()
	t.Logf("SSE body:\n%s", body)

	events := parseSSEEvents(strings.NewReader(body))
	found := false
	for _, evt := range events {
		if evt["event"] == "datastar-patch-signals" && strings.Contains(evt["data"], "invoice_42") {
			found = true
		}
	}
	if !found {
		t.Error("dynamic scope invoice:42 was not received after AddScope")
	}
}

func TestStreamHandler_MaxConnectionDuration(t *testing.T) {
	ps := newPubSub(t)
	broker := stream.NewBroker(ps, stream.WithMaxConnectionDuration(500*time.Millisecond))

	// Use a context without cancellation — the handler should exit on its own.
	req := httptest.NewRequest("GET", "/stream?scope=counter:shared", nil)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		broker.Handler().ServeHTTP(w, req)
	}()

	select {
	case <-done:
		// Handler exited on its own — good.
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not exit within expected max connection duration")
	}

	// Verify that invalidating after exit doesn't panic (subscriptions cleaned up).
	if err := broker.Invalidate("counter:shared"); err != nil {
		t.Fatalf("Invalidate after handler exit failed: %v", err)
	}
}

func TestSubscribeHandler(t *testing.T) {
	ps := newPubSub(t)
	broker := stream.NewBroker(ps)

	// Test missing scope
	req := httptest.NewRequest("POST", "/stream/subscribe", nil)
	wctx := &dsx.Context{SessionID: "sess1"}
	req = req.WithContext(wctx.WithContext(req.Context()))
	w := httptest.NewRecorder()
	broker.SubscribeHandler().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing scope, got %d", w.Code)
	}

	// Test missing session
	req = httptest.NewRequest("POST", "/stream/subscribe", strings.NewReader("scope=invoice:42"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	wctx2 := &dsx.Context{SessionID: ""}
	req = req.WithContext(wctx2.WithContext(req.Context()))
	w = httptest.NewRecorder()
	broker.SubscribeHandler().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing session, got %d", w.Code)
	}

	// Test successful subscribe
	req = httptest.NewRequest("POST", "/stream/subscribe", strings.NewReader("scope=invoice:42"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	wctx3 := &dsx.Context{SessionID: "sess1"}
	req = req.WithContext(wctx3.WithContext(req.Context()))
	w = httptest.NewRecorder()
	broker.SubscribeHandler().ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", w.Code)
	}

	// Test wrong method
	req = httptest.NewRequest("GET", "/stream/subscribe?scope=x", nil)
	wctx4 := &dsx.Context{SessionID: "sess1"}
	req = req.WithContext(wctx4.WithContext(req.Context()))
	w = httptest.NewRecorder()
	broker.SubscribeHandler().ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}
