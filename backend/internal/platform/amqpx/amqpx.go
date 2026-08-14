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
	JobsExchange          = "jobs.poll"
	MetricsQueue          = "metrics.raw"
	SyncResultsQueue      = "sync.results"
	DiscoveryResultsQueue = "discovery.results"
	EventsExchange        = "events.domain"
)

func SiteQueue(siteID string) string   { return "poll.site." + siteID }
func SiteRouting(siteID string) string { return "site." + siteID }

type Client struct {
	url  string
	conn *amqp.Connection
	ch   *amqp.Channel
	mu   sync.Mutex
}

// channel returns a live channel, redialing the connection and/or reopening
// the channel as needed. AMQP closes channels on channel-level exceptions
// (e.g. 406) and the whole connection on broker restarts; publisher paths
// self-heal here, while consumers treat a closed delivery stream as fatal
// (crash-only: the supervisor restarts the process — doc 23 §7).
func (c *Client) channel() (*amqp.Channel, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil || c.conn.IsClosed() {
		conn, err := amqp.Dial(c.url)
		if err != nil {
			return nil, errx.Wrap(errx.KindTransient, err, "amqpx: redial")
		}
		c.conn, c.ch = conn, nil
	}
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
			return &Client{url: url, conn: conn, ch: ch}, nil
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

// EnsureDiscoveryResultsQueue declares discovery.results (quorum).
func (c *Client) EnsureDiscoveryResultsQueue() error {
	ch, err := c.channel()
	if err != nil {
		return err
	}
	_, err = ch.QueueDeclare(DiscoveryResultsQueue, true, false, false, false,
		amqp.Table{"x-queue-type": "quorum"})
	return err
}

// EnsureEventsTopology declares the events.domain topic exchange (doc 05 §4).
func (c *Client) EnsureEventsTopology() error {
	ch, err := c.channel()
	if err != nil {
		return err
	}
	return ch.ExchangeDeclare(EventsExchange, "topic", true, false, false, false, nil)
}

// EnsureTopicQueue declares a quorum queue bound to events.domain by pattern.
func (c *Client) EnsureTopicQueue(queue, pattern string) error {
	ch, err := c.channel()
	if err != nil {
		return err
	}
	if _, err := ch.QueueDeclare(queue, true, false, false, false,
		amqp.Table{"x-queue-type": "quorum"}); err != nil {
		return err
	}
	return ch.QueueBind(queue, pattern, EventsExchange, false, nil)
}

// Consume starts delivering from a queue with manual acks. The returned stop
// function cancels the consumer and requeues anything it holds unacked; every
// caller must call it when it stops reading, and callers that reconnect in a
// loop must call it before consuming again.
//
// The consumer gets its own channel rather than the shared one, which is the
// whole point. Every caller of this consumes inside a reconnect loop, and on
// the shared channel a second Consume for the same queue registered a *second*
// consumer while the first stayed alive — the channel never closed, so the
// broker never cancelled it. RabbitMQ then round-robined deliveries to a
// consumer nobody was reading, where they sat unacked forever: the queue grew
// without bound while the service looked healthy and kept processing the
// fraction of jobs that happened to land on the live consumer.
//
// Seen on the pilot after a restart raced RabbitMQ: one site's queue held 19
// unacked messages across 3 consumers, and that site was quietly losing two
// polls out of three (doc 07 §6).
func (c *Client) Consume(queue string, prefetch int) (<-chan amqp.Delivery, func(), error) {
	conn, err := c.connection()
	if err != nil {
		return nil, nil, err
	}
	ch, err := conn.Channel()
	if err != nil {
		return nil, nil, errx.Wrap(errx.KindTransient, err, "amqpx: consume channel")
	}
	stop := func() { _ = ch.Close() }
	if err := ch.Qos(prefetch, 0, false); err != nil {
		stop()
		return nil, nil, errx.Wrap(errx.KindTransient, err, "amqpx: qos")
	}
	deliveries, err := ch.Consume(queue, "", false, false, false, false, nil)
	if err != nil {
		stop()
		return nil, nil, err
	}
	return deliveries, stop, nil
}

// connection returns a live connection, redialing if the previous one died.
// Shared-channel state is dropped on redial: a channel belongs to exactly one
// connection.
func (c *Client) connection() (*amqp.Connection, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil || c.conn.IsClosed() {
		conn, err := amqp.Dial(c.url)
		if err != nil {
			return nil, errx.Wrap(errx.KindTransient, err, "amqpx: redial")
		}
		c.conn, c.ch = conn, nil
	}
	return c.conn, nil
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
