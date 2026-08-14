# Multi-stage build for any netinv service: --build-arg SERVICE=api|scheduler|...
# Build context is the REPO ROOT (the Go workspace spans backend + connectors).
#
# Pinned to the exact patch, not the 1.26 line, and it has to stay >= the `go`
# directive in go.work. The alpine images set GOTOOLCHAIN=local, so the
# toolchain cannot download a newer one the way a local `go build` does: if
# go.work asks for more than the base image ships, the build fails with
#   go: ../go.work requires go >= 1.26.6 (running go 1.26.5; GOTOOLCHAIN=local)
# Floating on `1.26-alpine` hid that behind whenever you last pulled — CI was
# green on a fresh pull while a five-week-old local cache could not build at
# all. Bump this and go.work/go.mod together.
FROM golang:1.26.6-alpine AS build
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
