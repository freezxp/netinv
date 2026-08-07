// Package amqp — publishes alert.* events onto events.domain (doc 05 §8).
package amqp

import (
	"context"
	"strings"

	"github.com/freezxp/netinv/backend/internal/alerting/app"
	"github.com/freezxp/netinv/backend/internal/alerting/domain"
	"github.com/freezxp/netinv/backend/internal/platform/amqpx"
	"github.com/freezxp/netinv/backend/internal/platform/wire"
)

type Publisher struct {
	Client  *amqpx.Client
	BaseURL string // UI base for deep links (PRD G3), e.g. https://netinv.example
}

var _ app.AlertPublisher = (*Publisher)(nil)

func (p *Publisher) Publish(ctx context.Context, event string,
	inst *domain.Instance, rule *domain.Rule) error {
	ev := wire.AlertEvent{
		Event: event, AlertID: inst.ID, RuleID: rule.ID, RuleName: rule.Name,
		Severity: string(inst.Severity), State: string(inst.State),
		DeviceID: inst.DeviceID, Labels: inst.Labels, Value: inst.Value,
		FiredAt: inst.FiredAt, Summary: render(rule, inst),
	}
	if p.BaseURL != "" && inst.DeviceID != "" {
		ev.GraphURL = p.BaseURL + "/devices/" + inst.DeviceID + "?from=-3h"
		if ifIdx := inst.Labels["if_index"]; ifIdx != "" {
			ev.GraphURL += "&if=" + ifIdx
		}
	}
	return p.Client.PublishJSON(ctx, amqpx.EventsExchange, event, ev)
}

// render fills {{label}} placeholders in the rule's summary annotation.
func render(rule *domain.Rule, inst *domain.Instance) string {
	tpl := rule.Annotations["summary"]
	if tpl == "" {
		tpl = rule.Name
	}
	out := tpl
	for k, v := range inst.Labels {
		out = strings.ReplaceAll(out, "{{"+k+"}}", v)
	}
	// Convenience aliases used by the builtin pack.
	out = strings.ReplaceAll(out, "{{device}}", inst.Labels["device"])
	out = strings.ReplaceAll(out, "{{if_name}}", "if_index "+inst.Labels["if_index"])
	out = strings.ReplaceAll(out, "{{component}}", inst.Labels["component"])
	return out
}
