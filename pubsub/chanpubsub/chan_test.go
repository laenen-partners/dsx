package chanpubsub

import (
	"sync"
	"testing"
	"time"
)

func TestExactMatch(t *testing.T) {
	ps := New()
	defer func() {
		if err := ps.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	got := make(chan string, 1)
	_, err := ps.Subscribe("foo.bar", func(data []byte) { got <- string(data) })
	if err != nil {
		t.Fatal(err)
	}

	if err := ps.Publish("foo.bar", []byte("hello")); err != nil {
		t.Fatal(err)
	}

	select {
	case v := <-got:
		if v != "hello" {
			t.Errorf("got %q, want %q", v, "hello")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestNoMatch(t *testing.T) {
	ps := New()
	defer func() {
		if err := ps.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	got := make(chan struct{}, 1)
	_, err := ps.Subscribe("foo.bar", func([]byte) { got <- struct{}{} })
	if err != nil {
		t.Fatal(err)
	}

	if err := ps.Publish("foo.baz", nil); err != nil {
		t.Fatal(err)
	}

	select {
	case <-got:
		t.Fatal("should not have matched")
	case <-time.After(100 * time.Millisecond):
		// ok
	}
}

func TestWildcardStar(t *testing.T) {
	ps := New()
	defer func() {
		if err := ps.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	got := make(chan string, 2)
	_, err := ps.Subscribe("foo.*", func(data []byte) { got <- string(data) })
	if err != nil {
		t.Fatal(err)
	}

	if err := ps.Publish("foo.bar", []byte("a")); err != nil {
		t.Fatal(err)
	}

	if err := ps.Publish("foo.baz", []byte("b")); err != nil {
		t.Fatal(err)
	}

	if err := ps.Publish("foo.bar.deep", []byte("nope")); err != nil { // should not match
		t.Fatal(err)
	}

	var results []string
	for range 2 {
		select {
		case v := <-got:
			results = append(results, v)
		case <-time.After(time.Second):
			t.Fatal("timeout")
		}
	}

	select {
	case <-got:
		t.Fatal("deep topic should not match single wildcard")
	case <-time.After(100 * time.Millisecond):
	}

	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}
}

func TestWildcardGt(t *testing.T) {
	ps := New()
	defer func() {
		if err := ps.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	got := make(chan string, 3)
	_, err := ps.Subscribe("foo.>", func(data []byte) { got <- string(data) })
	if err != nil {
		t.Fatal(err)
	}

	if err := ps.Publish("foo.bar", []byte("a")); err != nil {
		t.Fatal(err)
	}
	if err := ps.Publish("foo.bar.baz", []byte("b")); err != nil {
		t.Fatal(err)
	}
	if err := ps.Publish("foo.x.y.z", []byte("c")); err != nil {
		t.Fatal(err)
	}

	for i := range 3 {
		select {
		case <-got:
		case <-time.After(time.Second):
			t.Fatalf("timeout on message %d", i)
		}
	}
}

func TestFanOut(t *testing.T) {
	ps := New()
	defer func() {
		if err := ps.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	var mu sync.Mutex
	counts := map[string]int{}

	_, err := ps.Subscribe("topic", func([]byte) {
		mu.Lock()
		counts["a"]++
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = ps.Subscribe("topic", func([]byte) {
		mu.Lock()
		counts["b"]++
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := ps.Publish("topic", nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if counts["a"] != 1 || counts["b"] != 1 {
		t.Errorf("expected both handlers called once, got %v", counts)
	}
}

func TestUnsubscribe(t *testing.T) {
	ps := New()
	defer func() {
		if err := ps.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	got := make(chan struct{}, 1)
	sub, err := ps.Subscribe("topic", func([]byte) { got <- struct{}{} })
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(100 * time.Millisecond)
	if err := sub.Unsubscribe(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)

	if err := ps.Publish("topic", nil); err != nil {
		t.Fatal(err)
	}

	select {
	case <-got:
		t.Fatal("should not receive after unsubscribe")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestUnsubscribeIdempotent(t *testing.T) {
	ps := New()
	defer func() {
		if err := ps.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	sub, _ := ps.Subscribe("topic", func([]byte) {})
	if err := sub.Unsubscribe(); err != nil {
		t.Fatal(err)
	}
	if err := sub.Unsubscribe(); err != nil {
		t.Fatal(err)
	}
}

func TestCloseStopsDelivery(t *testing.T) {
	ps := New()

	got := make(chan struct{}, 1)
	_, err := ps.Subscribe("topic", func([]byte) { got <- struct{}{} })
	if err != nil {
		t.Fatal(err)
	}

	if err := ps.Close(); err != nil {
		t.Fatal(err)
	}

	if err := ps.Publish("topic", nil); err != nil {
		t.Fatal(err)
	}

	select {
	case <-got:
		t.Fatal("should not deliver after close")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestMatchTopic(t *testing.T) {
	tests := []struct {
		pattern string
		topic   string
		want    bool
	}{
		{"a.b", "a.b", true},
		{"a.b", "a.c", false},
		{"a.*", "a.b", true},
		{"a.*", "a.b.c", false},
		{"a.>", "a.b", true},
		{"a.>", "a.b.c", true},
		{"a.>", "a.b.c.d", true},
		{">", "a", true},
		{">", "a.b", true},
		{"a.*.c", "a.b.c", true},
		{"a.*.c", "a.b.d", false},
		{"a", "a.b", false},
		{"a.b", "a", false},
	}
	for _, tt := range tests {
		t.Run(tt.pattern+"_"+tt.topic, func(t *testing.T) {
			if got := matchTopic(tt.pattern, tt.topic); got != tt.want {
				t.Errorf("matchTopic(%q, %q) = %v, want %v", tt.pattern, tt.topic, got, tt.want)
			}
		})
	}
}
