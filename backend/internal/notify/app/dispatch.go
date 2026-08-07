// Package app — Notification context: route alert events to channels
// (doc 05 §2 notifier; FR-NOT).
package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/retryx"
	"github.com/freezxp/netinv/backend/internal/platform/wire"
)

// Channel is a decrypted, ready-to-use notification target.
type Channel struct {
	ID     string
	Name   string
	Kind   string // email | webhook | slack
	Config map[string]any
	Secret map[string]string
}

type ChannelSource interface {
	EnabledChannels(ctx context.Context) ([]Channel, error)
}

// Sender delivers one rendered notification (doc 17 §4).
type Sender interface {
	Send(ctx context.Context, ch Channel, ev wire.AlertEvent) error
}

type DeliveryLog interface {
	Record(ctx context.Context, alertID, channelID, event, status string,
		attempts int, lastErr string)
}

type Dispatcher struct {
	Channels ChannelSource
	Senders  map[string]Sender // by kind
	Log      *slog.Logger
	Delivery DeliveryLog

	cache   []Channel
	cacheAt time.Time
}

// Handle routes one alert event. Default policy (FR-NOT-02): critical and
// warning notify all enabled channels; info notifies none. Resolved events
// follow the channel of the original severity.
func (d *Dispatcher) Handle(ctx context.Context, ev wire.AlertEvent) {
	if ev.Severity == "info" {
		return
	}
	channels, err := d.channels(ctx)
	if err != nil {
		d.Log.Error("load channels failed", "err", err)
		return
	}
	for _, ch := range channels {
		sender, ok := d.Senders[ch.Kind]
		if !ok {
			continue
		}
		attempts := 0
		err := retryx.Do(ctx, retryx.Policy{
			MaxAttempts: 5, BaseDelay: 2 * time.Second, MaxDelay: 60 * time.Second,
		}, func() error {
			attempts++
			return sender.Send(ctx, ch, ev)
		})
		status, lastErr := "ok", ""
		if err != nil {
			status, lastErr = "failed", err.Error()
			d.Log.Error("notification delivery failed", "channel", ch.Name,
				"kind", ch.Kind, "alert", ev.AlertID, "attempts", attempts, "err", err)
		} else {
			d.Log.Info("notification delivered", "channel", ch.Name,
				"kind", ch.Kind, "alert", ev.AlertID, "event", ev.Event)
		}
		if d.Delivery != nil {
			d.Delivery.Record(ctx, ev.AlertID, ch.ID, ev.Event, status, attempts, lastErr)
		}
	}
}

func (d *Dispatcher) channels(ctx context.Context) ([]Channel, error) {
	if time.Since(d.cacheAt) < time.Minute && d.cache != nil {
		return d.cache, nil
	}
	chs, err := d.Channels.EnabledChannels(ctx)
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "channels")
	}
	d.cache, d.cacheAt = chs, time.Now()
	return chs, nil
}
