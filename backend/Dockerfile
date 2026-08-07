# Multi-stage build for any netinv service: --build-arg SERVICE=api|scheduler|...
FROM golang:1.26-alpine AS build
ARG SERVICE=api
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X github.com/freezxp/netinv/backend/internal/platform/logx.Version=$(cat VERSION 2>/dev/null || echo dev)" \
    -o /out/netinv-svc ./cmd/${SERVICE}

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/netinv-svc /netinv-svc
USER nonroot
ENTRYPOINT ["/netinv-svc"]
