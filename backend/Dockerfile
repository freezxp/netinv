# Multi-stage build for any netinv service: --build-arg SERVICE=api|scheduler|...
# Build context is the REPO ROOT (the Go workspace spans backend + connectors).
FROM golang:1.26-alpine AS build
ARG SERVICE=api
WORKDIR /src
COPY go.work go.work.sum* ./
COPY backend/go.mod backend/go.sum backend/
COPY connectors/go.mod connectors/
RUN GOFLAGS=-mod=mod go mod download -C backend && go mod download -C connectors || true
COPY backend backend
COPY connectors connectors
RUN CGO_ENABLED=0 go build -C backend -trimpath \
    -ldflags "-s -w -X github.com/freezxp/netinv/backend/internal/platform/logx.Version=${SERVICE}" \
    -o /out/netinv-svc ./cmd/${SERVICE}

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/netinv-svc /netinv-svc
USER nonroot
ENTRYPOINT ["/netinv-svc"]
