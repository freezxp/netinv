// Package amqp — job publisher over amqpx (doc 05 §4).
package amqp

import (
	"context"

	"github.com/freezxp/netinv/backend/internal/collection/domain"
	"github.com/freezxp/netinv/backend/internal/platform/amqpx"
	"github.com/freezxp/netinv/backend/internal/platform/wire"
)

type JobPublisher struct{ Client *amqpx.Client }

func (p *JobPublisher) EnsureSiteQueue(siteID string) (domain.SiteQueueState, error) {
	st, err := p.Client.EnsureSiteQueue(siteID)
	return domain.SiteQueueState{Consumers: st.Consumers, Queued: st.Messages}, err
}

func (p *JobPublisher) Publish(ctx context.Context, siteID string, job wire.PollJob) error {
	return p.Client.PublishJSON(ctx, amqpx.JobsExchange, amqpx.SiteRouting(siteID), job)
}
