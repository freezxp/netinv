FROM node:22.23.4-alpine AS build
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY . .
# Stamped into the footer so a running deployment can say what it is. Defaults
# to package.json's version when the build does not pass one; release builds
# pass the tag (see docker-compose.yml).
ARG NETINV_VERSION=""
ENV NETINV_VERSION=$NETINV_VERSION
RUN npm run build

# Pinned to a minor line rather than :alpine, so a rebuild is reproducible, and
# bumped deliberately: 1.27-alpine sat on Alpine 3.21 and carried 35 HIGH/
# CRITICAL findings including two in OpenSSL — which matters directly here,
# because this container is what terminates TLS. Pinned to the patch release
# since 2026-11-20: 1.30.7 is the first stable release fixing NGINX2Root
# (CVE-2026-123007, an HTTP/2 RCE; TLS mode has `http2 on`), and the 1.29
# mainline line gets no further fixes. Do not go below 1.30.7.
FROM nginx:1.30.7-alpine
# Pick up package fixes published since the base image was cut. Without this a
# rebuild is only as current as the last time upstream rebuilt nginx, which for
# a security-relevant surface is not current enough.
RUN apk upgrade --no-cache
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /app/dist /usr/share/nginx/html
EXPOSE 80
