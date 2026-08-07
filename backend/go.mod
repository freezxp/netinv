module github.com/freezxp/netinv/backend

go 1.26

require (
	github.com/freezxp/netinv/connectors v0.0.0-00010101000000-000000000000
	github.com/go-chi/chi/v5 v5.3.1
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/gosnmp/gosnmp v1.44.0
	github.com/jackc/pgx/v5 v5.10.0
	github.com/oklog/ulid/v2 v2.1.2
	github.com/pressly/goose/v3 v3.27.3
	github.com/rabbitmq/amqp091-go v1.13.0
	github.com/redis/go-redis/v9 v9.22.0
	golang.org/x/crypto v0.54.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/mfridman/interpolate v0.0.2 // indirect
	github.com/sethvargo/go-retry v0.4.0 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
)

replace github.com/freezxp/netinv/connectors => ../connectors
