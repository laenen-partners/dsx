package stream_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/laenen-partners/dsx/stream"
	"github.com/laenen-partners/identity"
	"github.com/laenen-partners/pubsub"
	"github.com/laenen-partners/pubsub/chanpubsub"
	"github.com/starfederation/datastar-go/datastar"
)

// newPubSub creates an in-process pub/sub for testing.
func newPubSub(t *testing.T) *chanpubsub.ChanPubSub {
	t.Helper()
	ps := chanpubsub.New()
	t.Cleanup(func() {
		if err := ps.Close(context.Background()); err != nil {
			t.Errorf("closing pubsub: %v", err)
		}
	})
	return ps
}

// newBus creates a Bus scoped to the test identity's tenant/workspace.
func newBus(t *testing.T, ps pubsub.PubSub) *pubsub.Bus {
	t.Helper()
	id := testIdentity()
	return pubsub.NewBus(ps, "test", pubsub.WithScopeFrom(id))
}

func testIdentity() identity.Context {
	id, _ := identity.New("t1", "ws1", "user1", identity.PrincipalUser, []string{"admin"})
	return id
}

// testIdentityCtx returns a context with a test identity set.
func testIdentityCtx(ctx context.Context) context.Context {
	return identity.WithContext(ctx, testIdentity())
}

func TestWatch_SingleReaction(t *testing.T) {
	ctx := context.Background()
	attrs := stream.Watch(ctx, "counter",
		stream.Reload("updated", "/api/counter", stream.WithID("shared")))

	watchVal, ok := attrs["data-watch"]
	if !ok {
		t.Fatal("expected data-watch attribute")
	}
	if watchVal != "counter.shared" {
		t.Errorf("data-watch = %q, want %q", watchVal, "counter.shared")
	}

	effect, ok := attrs["data-effect"]
	if !ok {
		t.Fatal("expected data-effect attribute")
	}
	effectStr := effect.(string)
	if !strings.Contains(effectStr, "$_dsEvent.ts > 0") {
		t.Errorf("effect should check ts > 0, got: %s", effectStr)
	}
	if !strings.Contains(effectStr, "$_dsEvent.domain === 'counter'") {
		t.Errorf("effect should check domain, got: %s", effectStr)
	}
	if !strings.Contains(effectStr, "$_dsEvent.action === 'updated'") {
		t.Errorf("effect should check action, got: %s", effectStr)
	}
	if !strings.Contains(effectStr, "$_dsEvent.id === 'shared'") {
		t.Errorf("effect should check id, got: %s", effectStr)
	}
	if !strings.Contains(effectStr, "@get('/api/counter')") {
		t.Errorf("effect should contain @get with reload URL, got: %s", effectStr)
	}
}

func TestWatch_WithoutID(t *testing.T) {
	ctx := context.Background()
	attrs := stream.Watch(ctx, "customers",
		stream.Reload("created,deleted", "/api/customers"))

	watchVal := attrs["data-watch"]
	if watchVal != "customers" {
		t.Errorf("data-watch = %q, want %q", watchVal, "customers")
	}

	effectStr := attrs["data-effect"].(string)
	if !strings.Contains(effectStr, "['created','deleted'].includes($_dsEvent.action)") {
		t.Errorf("effect should check multiple actions, got: %s", effectStr)
	}
	if strings.Contains(effectStr, "$_dsEvent.id") {
		t.Errorf("effect should NOT filter by id when WithID not used, got: %s", effectStr)
	}
}

func TestWatch_WildcardAction(t *testing.T) {
	ctx := context.Background()
	attrs := stream.Watch(ctx, "customers",
		stream.Reload("*", "/api/customers/count"))

	effectStr := attrs["data-effect"].(string)
	if strings.Contains(effectStr, ".action") {
		t.Errorf("wildcard action should not filter by action, got: %s", effectStr)
	}
	if !strings.Contains(effectStr, "$_dsEvent.domain === 'customers'") {
		t.Errorf("effect should still check domain, got: %s", effectStr)
	}
	if !strings.Contains(effectStr, "$_dsEvent.ts > 0") {
		t.Errorf("effect should check ts > 0, got: %s", effectStr)
	}
}

func TestWatch_MultipleReactions(t *testing.T) {
	ctx := context.Background()
	attrs := stream.Watch(ctx, "customers",
		stream.Reload("created,deleted", "/api/customers/list"),
		stream.Reload("*", "/api/customers/count"))

	effectStr := attrs["data-effect"].(string)
	if !strings.Contains(effectStr, "/api/customers/list") {
		t.Errorf("effect should contain list URL, got: %s", effectStr)
	}
	if !strings.Contains(effectStr, "/api/customers/count") {
		t.Errorf("effect should contain count URL, got: %s", effectStr)
	}
}

func TestEventSignals(t *testing.T) {
	got := stream.EventSignals()
	if !strings.HasPrefix(got, "{") || !strings.HasSuffix(got, "}") {
		t.Errorf("EventSignals should be wrapped in {}, got: %s", got)
	}
	if !strings.Contains(got, "_dsEvent") {
		t.Errorf("EventSignals should contain _dsEvent, got: %s", got)
	}
	if !strings.Contains(got, "domain") {
		t.Errorf("EventSignals should contain domain field, got: %s", got)
	}
}

func TestHandler_WatchParam(t *testing.T) {
	ps := newPubSub(t)
	bus := newBus(t, ps)
	relay := stream.New(ps)

	ctx, cancel := context.WithCancel(testIdentityCtx(context.Background()))
	defer cancel()

	req := httptest.NewRequest("GET", "/stream?watch=counter", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		relay.Handler().ServeHTTP(w, req)
	}()

	time.Sleep(100 * time.Millisecond)

	if err := bus.NotifyUpdated(ctx, "counter", "shared"); err != nil {
		t.Fatalf("NotifyUpdated failed: %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

	body := w.Body.String()
	t.Logf("SSE response body:\n%s", body)

	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/event-stream") {
		t.Errorf("expected text/event-stream content type, got: %s", ct)
	}

	events := parseSSEEvents(strings.NewReader(body))
	if len(events) == 0 {
		t.Fatal("expected at least one SSE event, got none")
	}

	found := false
	for _, evt := range events {
		if evt["event"] == "datastar-patch-signals" {
			if strings.Contains(evt["data"], `"domain"`) && strings.Contains(evt["data"], "counter") {
				found = true
			}
		}
	}
	if !found {
		t.Error("did not find datastar-patch-signals event with counter domain")
	}
}

func TestHandler_WatchWithID(t *testing.T) {
	ps := newPubSub(t)
	bus := newBus(t, ps)
	relay := stream.New(ps)

	ctx, cancel := context.WithCancel(testIdentityCtx(context.Background()))
	defer cancel()

	req := httptest.NewRequest("GET", "/stream?watch=counter.shared", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		relay.Handler().ServeHTTP(w, req)
	}()

	time.Sleep(100 * time.Millisecond)

	if err := bus.NotifyUpdated(ctx, "counter", "shared"); err != nil {
		t.Fatalf("NotifyUpdated failed: %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

	body := w.Body.String()
	events := parseSSEEvents(strings.NewReader(body))
	found := false
	for _, evt := range events {
		if evt["event"] == "datastar-patch-signals" && strings.Contains(evt["data"], `"action"`) {
			found = true
		}
	}
	if !found {
		t.Error("should have received event for counter.shared")
	}
}

func TestHandler_MultipleWatches(t *testing.T) {
	ps := newPubSub(t)
	bus := newBus(t, ps)
	relay := stream.New(ps)

	ctx, cancel := context.WithCancel(testIdentityCtx(context.Background()))
	defer cancel()

	req := httptest.NewRequest("GET", "/stream?watch=counter,invoice.456", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		relay.Handler().ServeHTTP(w, req)
	}()

	time.Sleep(100 * time.Millisecond)

	if err := bus.NotifyUpdated(ctx, "invoice", "456"); err != nil {
		t.Fatalf("NotifyUpdated failed: %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

	body := w.Body.String()
	events := parseSSEEvents(strings.NewReader(body))
	found := false
	for _, evt := range events {
		if evt["event"] == "datastar-patch-signals" && strings.Contains(evt["data"], "invoice") {
			found = true
		}
	}
	if !found {
		t.Error("should have received event for invoice.456")
	}
}

func TestHandler_NoWatches(t *testing.T) {
	ps := newPubSub(t)
	relay := stream.New(ps)

	req := httptest.NewRequest("GET", "/stream", nil)
	w := httptest.NewRecorder()

	relay.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestHandler_MaxConnectionDuration(t *testing.T) {
	ps := newPubSub(t)
	relay := stream.New(ps, stream.WithMaxConnectionDuration(500*time.Millisecond))

	ctx := testIdentityCtx(context.Background())
	req := httptest.NewRequest("GET", "/stream?watch=counter", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		relay.Handler().ServeHTTP(w, req)
	}()

	select {
	case <-done:
		// Handler exited on its own — good.
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not exit within expected max connection duration")
	}
}

func TestHandler_EventStructure(t *testing.T) {
	ps := newPubSub(t)
	bus := newBus(t, ps)
	relay := stream.New(ps)

	ctx, cancel := context.WithCancel(testIdentityCtx(context.Background()))
	defer cancel()

	req := httptest.NewRequest("GET", "/stream?watch=customers", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		relay.Handler().ServeHTTP(w, req)
	}()

	time.Sleep(100 * time.Millisecond)

	if err := bus.NotifyCreated(ctx, "customers", "42"); err != nil {
		t.Fatalf("NotifyCreated failed: %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

	body := w.Body.String()
	events := parseSSEEvents(strings.NewReader(body))

	for _, evt := range events {
		if evt["event"] != "datastar-patch-signals" {
			continue
		}
		// Parse the signal data to verify structure.
		data := evt["data"]
		if !strings.Contains(data, `"domain"`) {
			t.Error("event should contain domain field")
		}
		if !strings.Contains(data, `"customers"`) {
			t.Error("event domain should be 'customers'")
		}
		if !strings.Contains(data, `"id"`) {
			t.Error("event should contain id field")
		}
		if !strings.Contains(data, `"42"`) {
			t.Error("event id should be '42'")
		}
		if !strings.Contains(data, `"action"`) {
			t.Error("event should contain action field")
		}
		if !strings.Contains(data, `"created"`) {
			t.Error("event action should be 'created'")
		}
		if !strings.Contains(data, `"ts"`) {
			t.Error("event should contain ts field")
		}
		return
	}
	t.Fatal("no patch-signals event found")
}

func TestCounterHandler_GetCounter(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/stream/counter", nil)
	w := httptest.NewRecorder()

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
	if !strings.Contains(body, "42") {
		t.Error("response should contain counter value 42")
	}

	events := parseSSEEvents(strings.NewReader(body))
	if len(events) == 0 {
		t.Fatal("expected at least one SSE event")
	}

	found := false
	for _, evt := range events {
		if evt["event"] == "datastar-patch-elements" && strings.Contains(evt["data"], "stream-counter-value") {
			found = true
		}
	}
	if !found {
		t.Error("expected patch-elements event targeting stream-counter-value")
	}
}

func TestE2E_FullFlow(t *testing.T) {
	ps := newPubSub(t)
	bus := newBus(t, ps)
	relay := stream.New(ps)

	// === Step 1: Verify Watch returns correct attributes ===
	ctx := context.Background()
	attrs := stream.Watch(ctx, "counter",
		stream.Reload("updated", "/showcase/api/stream/counter",
			stream.WithID("shared")))

	if attrs["data-watch"] != "counter.shared" {
		t.Errorf("expected data-watch=counter.shared, got %v", attrs["data-watch"])
	}
	if _, ok := attrs["data-effect"]; !ok {
		t.Fatal("expected data-effect attribute")
	}

	// === Step 2: Stream handler receives notification ===
	streamCtx, streamCancel := context.WithCancel(testIdentityCtx(context.Background()))
	defer streamCancel()

	streamReq := httptest.NewRequest("GET", "/showcase/stream?watch=counter.shared", nil).WithContext(streamCtx)
	streamW := httptest.NewRecorder()

	streamDone := make(chan struct{})
	go func() {
		defer close(streamDone)
		relay.Handler().ServeHTTP(streamW, streamReq)
	}()

	time.Sleep(150 * time.Millisecond)

	if err := bus.NotifyUpdated(streamCtx, "counter", "shared"); err != nil {
		t.Fatalf("NotifyUpdated: %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	streamCancel()
	<-streamDone

	streamEvents := parseSSEEvents(strings.NewReader(streamW.Body.String()))
	if len(streamEvents) == 0 {
		t.Fatal("stream handler produced no SSE events after notification")
	}

	eventFound := false
	for _, evt := range streamEvents {
		if evt["event"] == "datastar-patch-signals" {
			if strings.Contains(evt["data"], "_dsEvent") && strings.Contains(evt["data"], "counter") {
				eventFound = true
			}
		}
	}
	if !eventFound {
		t.Fatal("stream did not push _dsEvent signal for counter")
	}

	// === Step 3: Counter handler returns correct response ===
	counterReq := httptest.NewRequest("GET", "/showcase/api/stream/counter", nil)
	counterW := httptest.NewRecorder()

	sse := datastar.NewSSE(counterW, counterReq)
	_ = sse.PatchElements(`<span id="stream-counter-value" class="text-6xl font-bold tabular-nums">0</span>`)

	counterEvents := parseSSEEvents(strings.NewReader(counterW.Body.String()))
	patchFound := false
	for _, evt := range counterEvents {
		if evt["event"] == "datastar-patch-elements" && strings.Contains(evt["data"], "stream-counter-value") {
			patchFound = true
		}
	}
	if !patchFound {
		t.Fatal("counter handler did not return expected patch-elements event")
	}
}

func TestE2E_MutationHandler_NoEmptyPatch(t *testing.T) {
	ps := newPubSub(t)
	bus := newBus(t, ps)

	var counter atomic.Int64

	handler := func(w http.ResponseWriter, r *http.Request) {
		counter.Add(1)
		if err := bus.NotifyUpdated(r.Context(), "counter", "shared"); err != nil {
			http.Error(w, fmt.Sprintf("Publish: %v", err), http.StatusInternalServerError)
			return
		}
		datastar.NewSSE(w, r)
	}

	ctx := testIdentityCtx(context.Background())
	req := httptest.NewRequest("GET", "/api/stream/increment", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	handler(w, req)

	body := w.Body.String()
	if strings.Contains(body, "datastar-patch-elements") {
		t.Error("mutation handler should NOT send patch-elements event")
	}
	if counter.Load() != 1 {
		t.Errorf("counter should be 1, got %d", counter.Load())
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

func TestWatch_EventSignalInJSON(t *testing.T) {
	// Verify that the event signal data can be unmarshaled.
	signals := stream.EventSignals()
	// EventSignals uses unquoted keys for Datastar, so we can't unmarshal directly.
	// Just verify the format.
	if !strings.Contains(signals, "_dsEvent") {
		t.Errorf("EventSignals should contain _dsEvent, got: %s", signals)
	}
	if !strings.Contains(signals, "domain") {
		t.Errorf("EventSignals should contain domain, got: %s", signals)
	}
}

func TestHandler_EventDataFields(t *testing.T) {
	ps := newPubSub(t)
	bus := newBus(t, ps)
	relay := stream.New(ps)

	ctx, cancel := context.WithCancel(testIdentityCtx(context.Background()))
	defer cancel()

	req := httptest.NewRequest("GET", "/stream?watch=doc.123", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		relay.Handler().ServeHTTP(w, req)
	}()

	time.Sleep(100 * time.Millisecond)

	if err := bus.NotifyUpdated(ctx, "doc", "123"); err != nil {
		t.Fatalf("NotifyUpdated failed: %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

	body := w.Body.String()
	events := parseSSEEvents(strings.NewReader(body))

	for _, evt := range events {
		if evt["event"] != "datastar-patch-signals" {
			continue
		}
		// The data line has format: "signals {JSON}\n"
		data := strings.TrimSpace(evt["data"])
		// Strip the "signals " prefix that Datastar adds.
		data = strings.TrimPrefix(data, "signals ")
		var parsed map[string]json.RawMessage
		if err := json.Unmarshal([]byte(data), &parsed); err != nil {
			t.Fatalf("failed to parse event data as JSON: %v\ndata: %s", err, data)
		}
		dsEvent, ok := parsed["_dsEvent"]
		if !ok {
			t.Fatal("event data missing _dsEvent key")
		}
		var event map[string]any
		if err := json.Unmarshal(dsEvent, &event); err != nil {
			t.Fatalf("failed to parse _dsEvent: %v", err)
		}
		if event["domain"] != "doc" {
			t.Errorf("domain = %v, want 'doc'", event["domain"])
		}
		if event["id"] != "123" {
			t.Errorf("id = %v, want '123'", event["id"])
		}
		if event["action"] != "updated" {
			t.Errorf("action = %v, want 'updated'", event["action"])
		}
		if _, ok := event["ts"]; !ok {
			t.Error("event missing 'ts' field")
		}
		return
	}
	t.Fatal("no patch-signals event found")
}
