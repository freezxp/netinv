-- +goose Up
-- A device assigned to a site no poller serves is accepted, scheduled, and
-- then never polled. Nothing fails: the scheduler declares the site queue
-- itself before publishing, so every job is routable and simply accumulates
-- unread. The device stays 'pending' forever while ICMP-less, graph-less and
-- error-less — and because nothing failed, there is no failed sync run, no
-- log line, and nothing for the device page to show.
--
-- It is the quietest fault in the system and it was found the way quiet faults
-- are found: someone added devices to a new site, watched them sit in
-- 'pending', and had to be walked through counting consumers on a RabbitMQ
-- queue to learn why.
--
-- The signal already existed and was being discarded. AMQP's queue.declare
-- returns the queue's consumer and message counts, and the scheduler declares
-- every site queue it dispatches to. This table is where it now records what
-- that call told it, one row per site, so the API can answer "is anything
-- collecting for this site" without an AMQP connection of its own.
--
-- Deliberately not a metric: this has to be readable on the device detail page
-- in the same request that reads the device, and it has to be true on a
-- deployment with no pollers registered in platform.pollers at all — which is
-- the normal local-mode configuration, where the poller is given NETINV_SITE_ID
-- and never enrolls. A heartbeat-derived answer would report "no poller" for
-- every site on such a deployment, including the pilot.
CREATE TABLE platform.site_collection_health (
  site_id     text PRIMARY KEY REFERENCES platform.sites(id) ON DELETE CASCADE,
  consumers   integer NOT NULL,
  queued      integer NOT NULL,
  -- First moment this site was last seen with no consumer, cleared when one
  -- appears. A single observation is not a fault: a poller restarting is
  -- momentarily absent, and the scheduler ticks far faster than that resolves.
  no_consumer_since timestamptz,
  checked_at  timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS platform.site_collection_health;
