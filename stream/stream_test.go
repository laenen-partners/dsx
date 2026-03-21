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

func newBus(t *testing.T, ps pubsub.PubSub) *pubsub.Bus {
	t.Helper()
	id := testIdentity()
	return pubsub.NewBus(ps, "test", pubsub.WithScopeFrom(id))
}

func testIdentity() identity.Context {
	id, _ := identity.New("t1", "ws1", "user1", identity.PrincipalUser, []string{"admin"})
	return id
}

func testIdentityCtx(ctx context.Context) context.Context {
	return identity.WithContext(ctx, testIdentity())
}

func TestWatch_SingleReaction(t *testing.T) {
	ctx := context.Background()
	attrs := stream.Watch(ctx, "counter",
		stream.Updated.ID("shared").Get("/api/counter"))

	if watchVal := attrs["data-watch"]; watchVal != "counter.shared" {
		t.Errorf("data-watch = %q, want %q", watchVal, "counter.shared")
	}

	signalsStr := attrs["data-signals"].(string)
	if !strings.Contains(signalsStr, "_ds_counter") {
		t.Errorf("data-signals should contain _ds_counter, got: %s", signalsStr)
	}

	effectStr := attrs["data-effect"].(string)
	if !strings.Contains(effectStr, "$_ds_counter.ts > 0") {
		t.Errorf("effect should check ts > 0, got: %s", effectStr)
	}
	if !strings.Contains(effectStr, "'updated'") {
		t.Errorf("effect should check action, got: %s", effectStr)
	}
	if !strings.Contains(effectStr, "'connected'") {
		t.Errorf("effect should include 'connected' for reconnect, got: %s", effectStr)
	}
	if !strings.Contains(effectStr, "$_ds_counter.id === 'shared'") {
		t.Errorf("effect should check id, got: %s", effectStr)
	}
	if !strings.Contains(effectStr, "@get('/api/counter')") {
		t.Errorf("effect should contain @get URL, got: %s", effectStr)
	}
}

func TestWatch_Structural(t *testing.T) {
	ctx := context.Background()
	attrs := stream.Watch(ctx, "customers",
		stream.Structural.Get("/api/customers"))

	if watchVal := attrs["data-watch"]; watchVal != "customers" {
		t.Errorf("data-watch = %q, want %q", watchVal, "customers")
	}

	effectStr := attrs["data-effect"].(string)
	if !strings.Contains(effectStr, "'created'") || !strings.Contains(effectStr, "'deleted'") {
		t.Errorf("Structural should match created and deleted, got: %s", effectStr)
	}
	if !strings.Contains(effectStr, "'connected'") {
		t.Errorf("effect should include 'connected' for reconnect, got: %s", effectStr)
	}
	if strings.Contains(effectStr, "$_ds_customers.id") {
		t.Errorf("effect should NOT filter by id, got: %s", effectStr)
	}
}

func TestWatch_Or(t *testing.T) {
	ctx := context.Background()
	attrs := stream.Watch(ctx, "customers",
		stream.Created.Or(stream.Deleted).Get("/api/customers"))

	effectStr := attrs["data-effect"].(string)
	if !strings.Contains(effectStr, "'created'") || !strings.Contains(effectStr, "'deleted'") {
		t.Errorf("Or should combine actions, got: %s", effectStr)
	}
}

func TestWatch_WildcardAction(t *testing.T) {
	ctx := context.Background()
	attrs := stream.Watch(ctx, "customers",
		stream.Any.Get("/api/customers/count"))

	effectStr := attrs["data-effect"].(string)
	if strings.Contains(effectStr, ".includes(") {
		t.Errorf("wildcard should not filter by action, got: %s", effectStr)
	}
	if !strings.Contains(effectStr, "$_ds_customers.ts > 0") {
		t.Errorf("effect should check ts > 0, got: %s", effectStr)
	}
}

func TestWatch_MultipleReactions(t *testing.T) {
	ctx := context.Background()
	attrs := stream.Watch(ctx, "customers",
		stream.Structural.Get("/api/customers/list"),
		stream.Any.Get("/api/customers/count"))

	effectStr := attrs["data-effect"].(string)
	if !strings.Contains(effectStr, "/api/customers/list") {
		t.Errorf("effect should contain list URL, got: %s", effectStr)
	}
	if !strings.Contains(effectStr, "/api/customers/count") {
		t.Errorf("effect should contain count URL, got: %s", effectStr)
	}
}

func TestWatch_PerDomainSignals(t *testing.T) {
	ctx := context.Background()
	attrs := stream.Watch(ctx, "invoices",
		stream.Any.Get("/api/invoices"))

	signals := attrs["data-signals"].(string)
	if !strings.Contains(signals, "_ds_invoices") {
		t.Errorf("data-signals should use per-domain key, got: %s", signals)
	}
	if strings.Contains(signals, "_dsEvent") {
		t.Errorf("data-signals should NOT use old _dsEvent, got: %s", signals)
	}
}

func TestWatch_Debounce(t *testing.T) {
	ctx := context.Background()
	attrs := stream.Watch(ctx, "customers",
		stream.Structural.Debounce(300*time.Millisecond).Get("/api/customers/list"))

	effectStr := attrs["data-effect"].(string)
	if !strings.Contains(effectStr, "clearTimeout") {
		t.Errorf("debounced effect should contain clearTimeout, got: %s", effectStr)
	}
	if !strings.Contains(effectStr, "setTimeout") {
		t.Errorf("debounced effect should contain setTimeout, got: %s", effectStr)
	}
	if !strings.Contains(effectStr, "300") {
		t.Errorf("debounced effect should contain 300ms delay, got: %s", effectStr)
	}
	if !strings.Contains(effectStr, "@get('/api/customers/list')") {
		t.Errorf("debounced effect should contain @get URL, got: %s", effectStr)
	}
}

func TestWatch_NoDebounceByDefault(t *testing.T) {
	ctx := context.Background()
	attrs := stream.Watch(ctx, "customers",
		stream.Created.Get("/api/customers/list"))

	effectStr := attrs["data-effect"].(string)
	if strings.Contains(effectStr, "setTimeout") {
		t.Errorf("non-debounced effect should NOT contain setTimeout, got: %s", effectStr)
	}
}

func TestWatch_CustomAction(t *testing.T) {
	ctx := context.Background()
	attrs := stream.Watch(ctx, "orders",
		stream.Action("archived").Get("/api/orders"))

	effectStr := attrs["data-effect"].(string)
	if !strings.Contains(effectStr, "'archived'") {
		t.Errorf("effect should contain custom action 'archived', got: %s", effectStr)
	}
	if !strings.Contains(effectStr, "'connected'") {
		t.Errorf("effect should include 'connected' for reconnect, got: %s", effectStr)
	}
}

func TestWatch_CustomActionOr(t *testing.T) {
	ctx := context.Background()
	attrs := stream.Watch(ctx, "orders",
		stream.Created.Or(stream.Action("shipped")).Get("/api/orders"))

	effectStr := attrs["data-effect"].(string)
	if !strings.Contains(effectStr, "'created'") {
		t.Errorf("effect should contain 'created', got: %s", effectStr)
	}
	if !strings.Contains(effectStr, "'shipped'") {
		t.Errorf("effect should contain 'shipped', got: %s", effectStr)
	}
}

func TestSignalKey(t *testing.T) {
	if got := stream.SignalKey("customers"); got != "_ds_customers" {
		t.Errorf("SignalKey(customers) = %q, want _ds_customers", got)
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
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/event-stream") {
		t.Errorf("expected text/event-stream, got: %s", ct)
	}

	events := parseSSEEvents(strings.NewReader(body))
	found := false
	for _, evt := range events {
		if evt["event"] == "datastar-patch-signals" &&
			strings.Contains(evt["data"], "_ds_counter") {
			found = true
		}
	}
	if !found {
		t.Error("did not find datastar-patch-signals event with _ds_counter signal")
	}
}

func TestHandler_ConnectedEvent(t *testing.T) {
	ps := newPubSub(t)
	relay := stream.New(ps)

	ctx, cancel := context.WithCancel(testIdentityCtx(context.Background()))
	defer cancel()

	req := httptest.NewRequest("GET", "/stream?watch=counter,invoice", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		relay.Handler().ServeHTTP(w, req)
	}()

	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

	body := w.Body.String()
	events := parseSSEEvents(strings.NewReader(body))

	counterConnected, invoiceConnected := false, false
	for _, evt := range events {
		if evt["event"] == "datastar-patch-signals" {
			if strings.Contains(evt["data"], "_ds_counter") && strings.Contains(evt["data"], "connected") {
				counterConnected = true
			}
			if strings.Contains(evt["data"], "_ds_invoice") && strings.Contains(evt["data"], "connected") {
				invoiceConnected = true
			}
		}
	}
	if !counterConnected {
		t.Error("expected 'connected' event for counter domain")
	}
	if !invoiceConnected {
		t.Error("expected 'connected' event for invoice domain")
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

	events := parseSSEEvents(strings.NewReader(w.Body.String()))
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

	events := parseSSEEvents(strings.NewReader(w.Body.String()))
	found := false
	for _, evt := range events {
		if evt["event"] == "datastar-patch-signals" && strings.Contains(evt["data"], "_ds_invoice") {
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

	events := parseSSEEvents(strings.NewReader(w.Body.String()))
	for _, evt := range events {
		if evt["event"] != "datastar-patch-signals" {
			continue
		}
		data := evt["data"]
		if !strings.Contains(data, `"created"`) {
			continue
		}
		if !strings.Contains(data, `"_ds_customers"`) {
			t.Error("event should use per-domain signal key _ds_customers")
		}
		if !strings.Contains(data, `"42"`) {
			t.Error("event id should be '42'")
		}
		if !strings.Contains(data, `"ts"`) {
			t.Error("event should contain ts field")
		}
		return
	}
	t.Fatal("no patch-signals event with 'created' action found")
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

	ctx := context.Background()
	attrs := stream.Watch(ctx, "counter",
		stream.Updated.ID("shared").Get("/showcase/api/stream/counter"))

	if attrs["data-watch"] != "counter.shared" {
		t.Errorf("expected data-watch=counter.shared, got %v", attrs["data-watch"])
	}
	if _, ok := attrs["data-effect"]; !ok {
		t.Fatal("expected data-effect attribute")
	}
	if _, ok := attrs["data-signals"]; !ok {
		t.Fatal("expected data-signals attribute")
	}

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
	eventFound := false
	for _, evt := range streamEvents {
		if evt["event"] == "datastar-patch-signals" &&
			strings.Contains(evt["data"], "_ds_counter") &&
			strings.Contains(evt["data"], "updated") {
			eventFound = true
		}
	}
	if !eventFound {
		t.Fatal("stream did not push _ds_counter signal for counter")
	}

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

	if strings.Contains(w.Body.String(), "datastar-patch-elements") {
		t.Error("mutation handler should NOT send patch-elements event")
	}
	if counter.Load() != 1 {
		t.Errorf("counter should be 1, got %d", counter.Load())
	}
}

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

	events := parseSSEEvents(strings.NewReader(w.Body.String()))
	for _, evt := range events {
		if evt["event"] != "datastar-patch-signals" {
			continue
		}
		data := strings.TrimSpace(evt["data"])
		data = strings.TrimPrefix(data, "signals ")
		var parsed map[string]json.RawMessage
		if err := json.Unmarshal([]byte(data), &parsed); err != nil {
			continue
		}
		dsSignal, ok := parsed["_ds_doc"]
		if !ok {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal(dsSignal, &event); err != nil {
			t.Fatalf("failed to parse _ds_doc: %v", err)
		}
		if event["action"] == "connected" {
			continue
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
	t.Fatal("no patch-signals event found with _ds_doc signal")
}
