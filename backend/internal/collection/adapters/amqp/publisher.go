// Package amqp — job publisher over amqpx (doc 05 §4).
package amqp

import (
	"context"
	"time"

	"github.com/freezxp/netinv/backend/internal/collection/domain"
	"github.com/freezxp/netinv/backend/internal/platform/amqpx"
)

type JobPublisher struct{ Client *amqpx.Client }

func (p *JobPublisher) EnsureSiteQueue(siteID string, msgTTL time.Duration) error {
	return p.Client.EnsureSiteQueue(siteID, msgTTL)
}

func (p *JobPublisher) Publish(ctx context.Context, siteID string, job domain.PollJob) error {
	return p.Client.PublishJSON(ctx, amqpx.JobsExchange, amqpx.SiteRouting(siteID), job)
}
