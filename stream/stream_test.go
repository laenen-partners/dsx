package stream_test

import (
	"bufio"
	"bytes"
	"context"
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
	if !strings.HasPrefix(got, "{") || !strings.HasSuffix(got, "}") {
		t.Errorf("ScopeSignals should be wrapped in {}, got: %s", got)
	}
	if !strings.Contains(got, "_stream") {
		t.Errorf("ScopeSignals should contain _stream namespace, got: %s", got)
	}
	if !strings.Contains(got, "counter_shared") {
		t.Errorf("ScopeSignals should contain counter_shared key, got: %s", got)
	}
}

func TestWatchEffect(t *testing.T) {
	wctx := &dsx.Context{}
	ctx := wctx.WithContext(context.Background())

	effect := stream.WatchEffect(ctx, "counter:shared", "/api/counter")

	if len(wctx.Watchers) != 1 || wctx.Watchers[0].Scope != "counter:shared" {
		t.Errorf("WatchEffect should register watcher, got: %v", wctx.Watchers)
	}
	if !strings.Contains(effect, "$_stream.counter_shared") {
		t.Errorf("effect should reference $_stream.counter_shared, got: %s", effect)
	}
	if !strings.Contains(effect, "@get('/api/counter')") {
		t.Errorf("effect should contain @get with reload URL, got: %s", effect)
	}
}

func TestWatchEffect_UniqueKeys(t *testing.T) {
	wctx := &dsx.Context{}
	ctx := wctx.WithContext(context.Background())

	effect1 := stream.WatchEffect(ctx, "counter:shared", "/api/counter")
	effect2 := stream.WatchEffect(ctx, "counter:shared", "/api/count")

	if len(wctx.Watchers) != 2 {
		t.Fatalf("expected 2 watchers, got %d", len(wctx.Watchers))
	}
	if wctx.Watchers[0].Key == wctx.Watchers[1].Key {
		t.Errorf("watchers should have unique keys, both got: %s", wctx.Watchers[0].Key)
	}
	if effect1 == effect2 {
		t.Error("effects for same scope should have different signal keys")
	}
}

func TestRelayReceivesNotification(t *testing.T) {
	ps := newPubSub(t)
	bus := newBus(t, ps)
	relay := stream.New(ps)

	ctx, cancel := context.WithCancel(testIdentityCtx(context.Background()))
	defer cancel()

	req := httptest.NewRequest("GET", "/stream?scope=counter:shared", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		relay.Handler().ServeHTTP(w, req)
	}()

	time.Sleep(100 * time.Millisecond)

	// Publish via Bus — the Relay should pick this up.
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
		if evt["event"] == "datastar-patch-signals" && strings.Contains(evt["data"], "counter_shared") {
			found = true
		}
	}
	if !found {
		t.Error("did not find datastar-patch-signals event with counter_shared")
	}
}

func TestRelayWildcardScope(t *testing.T) {
	ps := newPubSub(t)
	bus := newBus(t, ps)
	relay := stream.New(ps)

	ctx, cancel := context.WithCancel(testIdentityCtx(context.Background()))
	defer cancel()

	req := httptest.NewRequest("GET", "/stream?scope=invoices:*", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		relay.Handler().ServeHTTP(w, req)
	}()

	time.Sleep(100 * time.Millisecond)

	// Publish to a specific invoice — wildcard subscriber should receive it.
	if err := bus.NotifyUpdated(ctx, "invoices", "42"); err != nil {
		t.Fatalf("NotifyUpdated failed: %v", err)
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
		t.Error("wildcard scope did not receive notification for invoices:42")
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
	relay := stream.New(ps)

	req := httptest.NewRequest("GET", "/stream", nil)
	w := httptest.NewRecorder()

	relay.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
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

func TestConnectTemplate_RendersWhenScopesExist(t *testing.T) {
	wctx := &dsx.Context{
		StreamURL: "/showcase/stream",
		Watchers:  []dsx.Watcher{{Scope: "counter:shared", Key: "counter_shared"}},
	}
	ctx := wctx.WithContext(context.Background())

	var buf bytes.Buffer
	if err := stream.Connect().Render(ctx, &buf); err != nil {
		t.Fatalf("Connect render failed: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, `style="display:none"`) {
		t.Error("Connect should render a hidden div")
	}
	if !strings.Contains(html, "data-signals=") {
		t.Error("Connect should have data-signals attribute")
	}
	if !strings.Contains(html, "_stream") {
		t.Error("data-signals should contain _stream namespace")
	}
	if !strings.Contains(html, "data-init") {
		t.Error("Connect should have data-init attribute")
	}
	if !strings.Contains(html, "/showcase/stream?counter=shared") {
		t.Error("Connect should have stream URL with grouped scope param")
	}
	if !strings.Contains(html, "requestCancellation") {
		t.Error("Connect should have requestCancellation option")
	}
}

func TestConnectTemplate_NoRenderWithoutScopes(t *testing.T) {
	wctx := &dsx.Context{StreamURL: "/showcase/stream"}
	ctx := wctx.WithContext(context.Background())

	var buf bytes.Buffer
	if err := stream.Connect().Render(ctx, &buf); err != nil {
		t.Fatalf("Connect render failed: %v", err)
	}
	if buf.Len() > 0 {
		t.Errorf("Connect should render nothing when no scopes, got: %s", buf.String())
	}
}

func TestConnectTemplate_NoRenderWithoutStreamURL(t *testing.T) {
	wctx := &dsx.Context{
		Watchers: []dsx.Watcher{{Scope: "counter:shared", Key: "counter_shared"}},
	}
	ctx := wctx.WithContext(context.Background())

	var buf bytes.Buffer
	if err := stream.Connect().Render(ctx, &buf); err != nil {
		t.Fatalf("Connect render failed: %v", err)
	}
	if buf.Len() > 0 {
		t.Errorf("Connect should render nothing when no StreamURL, got: %s", buf.String())
	}
}

func TestE2E_FullFlow(t *testing.T) {
	ps := newPubSub(t)
	bus := newBus(t, ps)
	relay := stream.New(ps)

	// === Step 1: Simulate page render ===
	wctx := &dsx.Context{
		BasePath:  "/showcase",
		StreamURL: "/showcase/stream",
	}
	ctx := wctx.WithContext(context.Background())

	effect := stream.WatchEffect(ctx, "counter:shared", "/showcase/api/stream/counter")
	t.Logf("Step 1 - WatchEffect returned: %s", effect)

	if len(wctx.Watchers) == 0 {
		t.Fatal("no watchers registered after WatchEffect")
	}

	// === Step 2: Render Connect template ===
	var connectBuf bytes.Buffer
	if err := stream.Connect().Render(ctx, &connectBuf); err != nil {
		t.Fatalf("Connect render: %v", err)
	}
	if connectBuf.Len() == 0 {
		t.Fatal("Connect rendered empty — stream won't connect")
	}

	// === Step 3: Stream handler receives notification ===
	streamCtx, streamCancel := context.WithCancel(testIdentityCtx(context.Background()))
	defer streamCancel()

	streamReq := httptest.NewRequest("GET", "/showcase/stream?scope=counter:shared", nil).WithContext(streamCtx)
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

	staleSignalFound := false
	for _, evt := range streamEvents {
		if evt["event"] == "datastar-patch-signals" {
			if strings.Contains(evt["data"], "counter_shared") && strings.Contains(evt["data"], "_stream") {
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

func TestE2E_DataSignalsFormat(t *testing.T) {
	signals := stream.ScopeSignals("counter:shared")

	if signals[0] != '{' || signals[len(signals)-1] != '}' {
		t.Errorf("ScopeSignals must be wrapped in {}, got: %s", signals)
	}
	if !strings.Contains(signals, "_stream") {
		t.Errorf("must contain _stream, got: %s", signals)
	}

	expected := `{_stream: {"counter_shared":false}}`
	if signals != expected {
		t.Errorf("ScopeSignals format mismatch\n  got:  %s\n  want: %s", signals, expected)
	}

	wctx := &dsx.Context{
		StreamURL: "/stream",
		Watchers:  []dsx.Watcher{{Scope: "counter:shared", Key: "counter_shared"}},
	}
	ctx := wctx.WithContext(context.Background())

	var buf bytes.Buffer
	if err := stream.Connect().Render(ctx, &buf); err != nil {
		t.Fatalf("Connect render: %v", err)
	}
	if !strings.Contains(buf.String(), "_stream") {
		t.Error("Connect HTML should contain _stream")
	}
}

func TestE2E_MultipleScopes(t *testing.T) {
	ps := newPubSub(t)
	bus := newBus(t, ps)
	relay := stream.New(ps)

	wctx := &dsx.Context{StreamURL: "/stream"}
	ctx := wctx.WithContext(context.Background())

	stream.WatchEffect(ctx, "counter:shared", "/api/counter")
	stream.WatchEffect(ctx, "invoice:42", "/api/invoice/42")

	if len(wctx.Watchers) != 2 {
		t.Fatalf("expected 2 watchers, got %d", len(wctx.Watchers))
	}

	var buf bytes.Buffer
	if err := stream.Connect().Render(ctx, &buf); err != nil {
		t.Fatalf("Connect render: %v", err)
	}
	html := buf.String()
	if !strings.Contains(html, "counter=shared") || !strings.Contains(html, "invoice=42") {
		t.Error("missing grouped scope params")
	}

	streamCtx, cancel := context.WithCancel(testIdentityCtx(context.Background()))
	defer cancel()

	req := httptest.NewRequest("GET", "/stream?scope=counter:shared&scope=invoice:42", nil).WithContext(streamCtx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		relay.Handler().ServeHTTP(w, req)
	}()

	time.Sleep(150 * time.Millisecond)

	// Publish only invoice:42
	if err := bus.NotifyUpdated(streamCtx, "invoice", "42"); err != nil {
		t.Fatalf("NotifyUpdated: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	cancel()
	<-done

	events := parseSSEEvents(strings.NewReader(w.Body.String()))
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

func TestMultiWatcher_SameScopeBothReceive(t *testing.T) {
	ps := newPubSub(t)
	bus := newBus(t, ps)
	relay := stream.New(ps)

	keysJSON := `{"customers:*":["customers_WILD","customers_WILD_2"]}`

	streamCtx, cancel := context.WithCancel(testIdentityCtx(context.Background()))
	defer cancel()

	req := httptest.NewRequest("GET",
		"/stream?customers=*&keys="+url.QueryEscape(keysJSON), nil).WithContext(streamCtx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		relay.Handler().ServeHTTP(w, req)
	}()

	time.Sleep(150 * time.Millisecond)

	if err := bus.NotifyCreated(streamCtx, "customers", "42"); err != nil {
		t.Fatalf("NotifyCreated: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	cancel()
	<-done

	body := w.Body.String()
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
		t.Errorf("expected both customers_WILD and customers_WILD_2, got key1=%v key2=%v", found1, found2)
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
			bus := newBus(t, ps)
			relay := stream.New(ps)

			ctx, cancel := context.WithCancel(testIdentityCtx(context.Background()))

			req := httptest.NewRequest("GET", tt.url, nil).WithContext(ctx)
			w := httptest.NewRecorder()

			done := make(chan struct{})
			go func() {
				defer close(done)
				relay.Handler().ServeHTTP(w, req)
			}()

			time.Sleep(100 * time.Millisecond)

			// Publish for all expected scopes
			for _, scope := range tt.want {
				entity, entityID, _ := strings.Cut(scope, ":")
				if err := bus.NotifyUpdated(ctx, entity, entityID); err != nil {
					t.Fatalf("NotifyUpdated %q: %v", scope, err)
				}
			}

			time.Sleep(200 * time.Millisecond)
			cancel()
			<-done

			body := w.Body.String()
			events := parseSSEEvents(strings.NewReader(body))

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

func TestStreamHandler_StaleOnlyNoData(t *testing.T) {
	ps := newPubSub(t)
	bus := newBus(t, ps)
	relay := stream.New(ps)

	ctx, cancel := context.WithCancel(testIdentityCtx(context.Background()))
	defer cancel()

	req := httptest.NewRequest("GET", "/stream?scope=counter:shared", nil).WithContext(ctx)
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
	foundStale := false
	for _, evt := range events {
		if evt["event"] == "datastar-patch-signals" {
			if strings.Contains(evt["data"], "_stream") && strings.Contains(evt["data"], "counter_shared") {
				foundStale = true
			}
		}
	}
	if !foundStale {
		t.Error("expected stale signal for counter_shared")
	}
}

func TestStreamHandler_MaxConnectionDuration(t *testing.T) {
	ps := newPubSub(t)
	relay := stream.New(ps, stream.WithMaxConnectionDuration(500*time.Millisecond))

	ctx := testIdentityCtx(context.Background())
	req := httptest.NewRequest("GET", "/stream?scope=counter:shared", nil).WithContext(ctx)
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
