// Package natspubsub adapts a *nats.Conn to the pubsub.PubSub interface.
package natspubsub

import (
	"github.com/laenen-partners/dsx/pubsub"
	"github.com/nats-io/nats.go"
)

// NatsPubSub wraps a NATS connection as a PubSub.
type NatsPubSub struct {
	conn *nats.Conn
}

// New creates a PubSub backed by an existing NATS connection.
func New(conn *nats.Conn) *NatsPubSub {
	return &NatsPubSub{conn: conn}
}

func (n *NatsPubSub) Publish(topic string, data []byte) error {
	return n.conn.Publish(topic, data)
}

func (n *NatsPubSub) Subscribe(topic string, handler func([]byte)) (pubsub.Subscription, error) {
	return n.conn.Subscribe(topic, func(msg *nats.Msg) {
		handler(msg.Data)
	})
}

func (n *NatsPubSub) Close() error {
	return n.conn.Drain()
}
