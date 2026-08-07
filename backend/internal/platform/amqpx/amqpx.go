// Package amqpx — RabbitMQ plumbing per the doc 05 §4 topology: publisher
// confirms, quorum queues, per-site job routing.
package amqpx

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/freezxp/netinv/backend/internal/platform/errx"
)

const (
	JobsExchange   = "jobs.poll"
	MetricsQueue   = "metrics.raw"
	EventsExchange = "events.domain"
)

func SiteQueue(siteID string) string  { return "poll.site." + siteID }
func SiteRouting(siteID string) string { return "site." + siteID }

type Client struct {
	conn *amqp.Connection
	ch   *amqp.Channel
}

// Connect dials AMQP, dependency-patient (doc 23 §7).
func Connect(ctx context.Context, url string) (*Client, error) {
	for {
		conn, err := amqp.Dial(url)
		if err == nil {
			ch, err := conn.Channel()
			if err != nil {
				_ = conn.Close()
				return nil, errx.Wrap(errx.KindTransient, err, "amqpx: channel")
			}
			if err := ch.Confirm(false); err != nil {
				_ = conn.Close()
				return nil, errx.Wrap(errx.KindTransient, err, "amqpx: confirm mode")
			}
			return &Client{conn: conn, ch: ch}, nil
		}
		select {
		case <-ctx.Done():
			return nil, errx.Wrap(errx.KindTransient, ctx.Err(), "amqpx: waiting for broker")
		case <-time.After(time.Second):
		}
	}
}

func (c *Client) Close() {
	if c.ch != nil {
		_ = c.ch.Close()
	}
	if c.conn != nil {
		_ = c.conn.Close()
	}
}

// DeclareJobTopology declares the jobs exchange; queues are per site.
func (c *Client) DeclareJobTopology() error {
	return c.ch.ExchangeDeclare(JobsExchange, "direct", true, false, false, false, nil)
}

// EnsureSiteQueue declares poll.site.<id> (quorum, job-TTL per doc 05 §4)
// and binds it to the jobs exchange.
func (c *Client) EnsureSiteQueue(siteID string, msgTTL time.Duration) error {
	args := amqp.Table{
		"x-queue-type":  "quorum",
		"x-message-ttl": int32(msgTTL.Milliseconds()),
	}
	if _, err := c.ch.QueueDeclare(SiteQueue(siteID), true, false, false, false, args); err != nil {
		return fmt.Errorf("amqpx: declare %s: %w", SiteQueue(siteID), err)
	}
	return c.ch.QueueBind(SiteQueue(siteID), SiteRouting(siteID), JobsExchange, false, nil)
}

// PublishJSON publishes with a confirm; returns once the broker accepts it.
func (c *Client) PublishJSON(ctx context.Context, exchange, routingKey string, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return errx.Wrap(errx.KindInternal, err, "amqpx: marshal")
	}
	conf, err := c.ch.PublishWithDeferredConfirmWithContext(ctx, exchange, routingKey,
		false, false, amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent,
			Timestamp:    time.Now(),
			Body:         body,
		})
	if err != nil {
		return errx.Wrap(errx.KindTransient, err, "amqpx: publish")
	}
	ok, err := conf.WaitContext(ctx)
	if err != nil {
		return errx.Wrap(errx.KindTransient, err, "amqpx: confirm wait")
	}
	if !ok {
		return errx.New(errx.KindTransient, "amqpx: broker nacked publish")
	}
	return nil
}
