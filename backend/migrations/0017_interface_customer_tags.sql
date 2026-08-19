-- +goose Up
-- Interfaces carry what the device says about them and nothing an operator
-- knows. ifAlias is the usual dumping ground for that — circuit ids, customer
-- names, ticket numbers, all crammed into one string a network engineer typed
-- into a router years ago — and it is the device's field: a reprovision, a
-- config restore or a different engineer overwrites it, and the sync dutifully
-- follows. Reporting "how much did this customer use" off a substring of
-- ifAlias works right up until it doesn't.
--
-- customer and tags are operator-owned. Sync never writes them: the interface
-- upsert names its columns and these are not among them, so a port can be
-- renamed, re-aliased or renumbered without losing who it belongs to.
--
-- customer is a column rather than a tag because it is the axis reports group
-- by, and "the tag that happens to be a customer name" is not something a
-- GROUP BY can express. Tags stay free-form for everything else — service
-- class, contract, site role — and deliberately have no vocabulary.
ALTER TABLE inventory.interfaces
  ADD COLUMN customer text,
  ADD COLUMN tags jsonb NOT NULL DEFAULT '[]';

-- Case-insensitive exact lookup drives the report filter ("this customer");
-- the trigram index drives the search box ("anything like this"). Both matter:
-- an invoice needs the exact set, a human hunting a circuit needs the fuzzy one.
CREATE INDEX interfaces_customer_lower_idx
  ON inventory.interfaces (lower(customer)) WHERE customer IS NOT NULL;
CREATE INDEX interfaces_customer_trgm
  ON inventory.interfaces USING gin (customer gin_trgm_ops);

-- +goose Down
DROP INDEX IF EXISTS inventory.interfaces_customer_trgm;
DROP INDEX IF EXISTS inventory.interfaces_customer_lower_idx;
ALTER TABLE inventory.interfaces DROP COLUMN IF EXISTS tags;
ALTER TABLE inventory.interfaces DROP COLUMN IF EXISTS customer;
