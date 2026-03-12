// Package redispubsub adapts a Redis client to the pubsub.PubSub interface
// using PUBLISH / SUBSCRIBE / PSUBSCRIBE.
//
// Wildcard topics (* and >) are translated to Redis glob patterns and use
// PSUBSCRIBE. Exact topics use SUBSCRIBE.
package redispubsub

import (
	"context"
	"strings"
	"sync"

	"github.com/laenen-partners/dsx/pubsub"
	"github.com/redis/go-redis/v9"
)

// RedisPubSub wraps a Redis client as a PubSub.
type RedisPubSub struct {
	client *redis.Client
	ctx    context.Context
	cancel context.CancelFunc

	mu   sync.Mutex
	subs []*redisSub
}

// New creates a PubSub backed by a Redis client.
func New(client *redis.Client) *RedisPubSub {
	ctx, cancel := context.WithCancel(context.Background())
	return &RedisPubSub{
		client: client,
		ctx:    ctx,
		cancel: cancel,
	}
}

func (r *RedisPubSub) Publish(topic string, data []byte) error {
	return r.client.Publish(r.ctx, topic, data).Err()
}

func (r *RedisPubSub) Subscribe(topic string, handler func([]byte)) (pubsub.Subscription, error) {
	var ps *redis.PubSub
	if needsPattern(topic) {
		pattern := translatePattern(topic)
		ps = r.client.PSubscribe(r.ctx, pattern)
	} else {
		ps = r.client.Subscribe(r.ctx, topic)
	}

	// Wait for the subscription to be confirmed.
	if _, err := ps.Receive(r.ctx); err != nil {
		if err := ps.Close(); err != nil {
			return nil, err
		}

		return nil, err
	}

	sub := &redisSub{ps: ps}
	r.mu.Lock()
	r.subs = append(r.subs, sub)
	r.mu.Unlock()

	go func() {
		ch := ps.Channel()
		for msg := range ch {
			handler([]byte(msg.Payload))
		}
	}()

	return sub, nil
}

func (r *RedisPubSub) Close() error {
	r.cancel()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.subs {
		if err := s.Unsubscribe(); err != nil {
			return err
		}
	}
	r.subs = nil
	return nil
}

type redisSub struct {
	ps   *redis.PubSub
	once sync.Once
}

func (s *redisSub) Unsubscribe() error {
	var err error
	s.once.Do(func() { err = s.ps.Close() })
	return err
}

// needsPattern reports whether the topic contains wildcards.
func needsPattern(topic string) bool {
	return strings.ContainsAny(topic, "*>")
}

// translatePattern converts NATS-style wildcards to Redis glob patterns.
//
//   - (one segment) → [^.]* (any chars except dot)
//     > (rest)        → *     (anything)
func translatePattern(topic string) string {
	parts := strings.Split(topic, ".")
	for i, p := range parts {
		switch p {
		case "*":
			parts[i] = "[^.]*"
		case ">":
			parts[i] = "*"
		}
	}
	return strings.Join(parts, ".")
}
