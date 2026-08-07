// netinv-notifier — email/webhook/Slack delivery with retries (doc 05 §2).
package main

import (
	"context"
	"encoding/json"
	"os"

	notifpg "github.com/freezxp/netinv/backend/internal/notify/adapters/postgres"
	"github.com/freezxp/netinv/backend/internal/notify/adapters/senders"
	notifapp "github.com/freezxp/netinv/backend/internal/notify/app"
	"github.com/freezxp/netinv/backend/internal/platform/amqpx"
	"github.com/freezxp/netinv/backend/internal/platform/cryptox"
	"github.com/freezxp/netinv/backend/internal/platform/pgx"
	"github.com/freezxp/netinv/backend/internal/platform/service"
	"github.com/freezxp/netinv/backend/internal/platform/wire"
)

const alertsQueue = "notify.alerts"

func main() {
	service.Run("notifier", func(ctx context.Context, rt *service.Runtime) error {
		dsn := os.Getenv("NETINV_PG_DSN")
		amqpURL := os.Getenv("NETINV_AMQP_URL")
		if dsn == "" || amqpURL == "" {
			rt.Log.Warn("NETINV_PG_DSN/NETINV_AMQP_URL not set — idle skeleton mode")
			rt.Health.SetReady(true)
			<-ctx.Done()
			return nil
		}
		keys, err := cryptox.LoadEnvMasterKey()
		if err != nil {
			return err // channel secrets need the master key (ADR-011)
		}
		pool, err := pgx.Connect(ctx, dsn)
		if err != nil {
			return err
		}
		defer pool.Close()
		mq, err := amqpx.Connect(ctx, amqpURL)
		if err != nil {
			return err
		}
		defer mq.Close()
		if err := mq.EnsureEventsTopology(); err != nil {
			return err
		}
		if err := mq.EnsureTopicQueue(alertsQueue, "alert.*"); err != nil {
			return err
		}
		deliveries, err := mq.Consume(alertsQueue, 8)
		if err != nil {
			return err
		}

		dispatcher := &notifapp.Dispatcher{
			Channels: &notifpg.ChannelRepo{Pool: pool, Keys: keys},
			Senders: map[string]notifapp.Sender{
				"email":   senders.Email{},
				"webhook": senders.Webhook{},
				"slack":   senders.Slack{},
			},
			Delivery: &notifpg.DeliveryRepo{Pool: pool, Log: rt.Log},
			Log:      rt.Log,
		}
		rt.Health.SetReady(true)
		rt.Log.Info("notifier consuming", "queue", alertsQueue)
		for {
			select {
			case <-ctx.Done():
				return nil
			case d, ok := <-deliveries:
				if !ok {
					return nil
				}
				var ev wire.AlertEvent
				if err := json.Unmarshal(d.Body, &ev); err != nil {
					rt.Log.Warn("malformed alert event dropped", "err", err)
					_ = d.Reject(false)
					continue
				}
				dispatcher.Handle(ctx, ev) // outcome recorded per channel
				_ = d.Ack(false)
			}
		}
	})
}
