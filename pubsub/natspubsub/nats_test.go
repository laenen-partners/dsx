package natspubsub_test

import (
	"context"
	"testing"

	"github.com/laenen-partners/dsx/pubsub/natspubsub"
	"github.com/laenen-partners/dsx/pubsub/pubsubtest"
	"github.com/nats-io/nats.go"
	tcnats "github.com/testcontainers/testcontainers-go/modules/nats"
)

func TestSuite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()

	container, err := tcnats.Run(ctx, "nats:2-alpine")
	if err != nil {
		t.Fatalf("starting NATS container: %v", err)
	}
	t.Cleanup(func() {
		if err := container.Terminate(ctx); err != nil {
			t.Fatalf("terminating NATS container: %v", err)
		}
	})

	url, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("getting NATS connection string: %v", err)
	}

	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("connecting to NATS: %v", err)
	}
	t.Cleanup(nc.Close)

	ps := natspubsub.New(nc)
	pubsubtest.RunSuite(t, ps)
}
