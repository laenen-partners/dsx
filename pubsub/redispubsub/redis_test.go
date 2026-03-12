package redispubsub_test

import (
	"context"
	"testing"

	"github.com/laenen-partners/dsx/pubsub/pubsubtest"
	"github.com/laenen-partners/dsx/pubsub/redispubsub"
	"github.com/redis/go-redis/v9"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

func TestSuite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()

	container, err := tcredis.Run(ctx, "redis:7-alpine")
	if err != nil {
		t.Fatalf("starting Redis container: %v", err)
	}
	t.Cleanup(func() {
		if err := container.Terminate(ctx); err != nil {
			t.Errorf("terminating Redis container: %v", err)
		}
	})

	endpoint, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("getting Redis connection string: %v", err)
	}

	opts, err := redis.ParseURL(endpoint)
	if err != nil {
		t.Fatalf("parsing Redis URL: %v", err)
	}

	client := redis.NewClient(opts)
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("closing Redis client: %v", err)
		}
	})

	ps := redispubsub.New(client)
	t.Cleanup(func() {
		if err := ps.Close(); err != nil {
			t.Errorf("closing Redis pubsub: %v", err)
		}
	})

	pubsubtest.RunSuite(t, ps)
}
