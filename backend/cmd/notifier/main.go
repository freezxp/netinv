// netinv-notifier — email/webhook/Slack delivery with retries (doc 05 §2).
package main

import (
	"context"
	"encoding/json"
	"os"
	"time"

	amqp091 "github.com/rabbitmq/amqp091-go"

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
		// Consume with reconnect across broker restarts (doc 23 §7).
		for ctx.Err() == nil {
			err := mq.EnsureEventsTopology()
			if err == nil {
				err = mq.EnsureTopicQueue(alertsQueue, "alert.*")
			}
			if err == nil {
				var deliveries <-chan amqp091.Delivery
				var stop func()
				deliveries, stop, err = mq.Consume(alertsQueue, 8)
				if err == nil {
					rt.Log.Info("notifier consuming", "queue", alertsQueue)
				stream:
					for {
						select {
						case <-ctx.Done():
							stop()
							return nil
						case d, ok := <-deliveries:
							if !ok {
								rt.Log.Warn("alert stream closed — reconnecting")
								break stream
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
					// Cancel before looping round to consume again: a leaked
					// consumer keeps receiving alerts nobody delivers or acks.
					stop()
				}
			}
			if err != nil {
				rt.Log.Warn("alert stream unavailable — retrying", "err", err)
			}
			select {
			case <-ctx.Done():
			case <-time.After(3 * time.Second):
			}
		}
		return nil
	})
}
