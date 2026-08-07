// Package amqpx — RabbitMQ plumbing per the doc 05 §4 topology: publisher
// confirms, quorum queues, per-site job routing.
package amqpx

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/freezxp/netinv/backend/internal/platform/errx"
)

const (
	JobsExchange     = "jobs.poll"
	MetricsQueue     = "metrics.raw"
	SyncResultsQueue = "sync.results"
	EventsExchange   = "events.domain"
)

func SiteQueue(siteID string) string  { return "poll.site." + siteID }
func SiteRouting(siteID string) string { return "site." + siteID }

type Client struct {
	conn *amqp.Connection
	ch   *amqp.Channel
	mu   sync.Mutex
}

// channel returns a live channel, reopening it if a previous channel-level
// exception (e.g. a 406 precondition failure) closed it. AMQP closes the
// channel on such errors; without recovery every later operation 504s.
func (c *Client) channel() (*amqp.Channel, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ch != nil && !c.ch.IsClosed() {
		return c.ch, nil
	}
	ch, err := c.conn.Channel()
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "amqpx: reopen channel")
	}
	if err := ch.Confirm(false); err != nil {
		_ = ch.Close()
		return nil, errx.Wrap(errx.KindTransient, err, "amqpx: confirm mode")
	}
	c.ch = ch
	return ch, nil
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
	ch, err := c.channel()
	if err != nil {
		return err
	}
	return ch.ExchangeDeclare(JobsExchange, "direct", true, false, false, false, nil)
}

// EnsureSiteQueue declares poll.site.<id> (quorum, fixed 15-minute stale-job
// TTL — one policy for all sites so every declarer agrees on queue args) and
// binds it to the jobs exchange.
func (c *Client) EnsureSiteQueue(siteID string) error {
	ch, err := c.channel()
	if err != nil {
		return err
	}
	args := amqp.Table{
		"x-queue-type":  "quorum",
		"x-message-ttl": int32((15 * time.Minute).Milliseconds()),
	}
	if _, err := ch.QueueDeclare(SiteQueue(siteID), true, false, false, false, args); err != nil {
		return fmt.Errorf("amqpx: declare %s: %w", SiteQueue(siteID), err)
	}
	return ch.QueueBind(SiteQueue(siteID), SiteRouting(siteID), JobsExchange, false, nil)
}

// EnsureMetricsQueue declares metrics.raw (quorum — doc 05 §4).
func (c *Client) EnsureMetricsQueue() error {
	ch, err := c.channel()
	if err != nil {
		return err
	}
	_, err = ch.QueueDeclare(MetricsQueue, true, false, false, false,
		amqp.Table{"x-queue-type": "quorum"})
	return err
}

// EnsureSyncResultsQueue declares sync.results (quorum — doc 05 §4).
func (c *Client) EnsureSyncResultsQueue() error {
	ch, err := c.channel()
	if err != nil {
		return err
	}
	_, err = ch.QueueDeclare(SyncResultsQueue, true, false, false, false,
		amqp.Table{"x-queue-type": "quorum"})
	return err
}

// Consume starts delivering from a queue with manual acks.
func (c *Client) Consume(queue string, prefetch int) (<-chan amqp.Delivery, error) {
	ch, err := c.channel()
	if err != nil {
		return nil, err
	}
	if err := ch.Qos(prefetch, 0, false); err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "amqpx: qos")
	}
	return ch.Consume(queue, "", false, false, false, false, nil)
}

// PublishJSON publishes with a confirm; returns once the broker accepts it.
func (c *Client) PublishJSON(ctx context.Context, exchange, routingKey string, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return errx.Wrap(errx.KindInternal, err, "amqpx: marshal")
	}
	ch, err := c.channel()
	if err != nil {
		return err
	}
	conf, err := ch.PublishWithDeferredConfirmWithContext(ctx, exchange, routingKey,
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
