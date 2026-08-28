# syntax=docker/dockerfile:1.7
# Multi-stage, non-root, no build toolchain in the runtime image.

FROM node:22.12.0-alpine3.20 AS web
WORKDIR /src
COPY panel/web/package.json panel/web/package-lock.json* ./
RUN npm ci --no-audit --no-fund || npm install --no-audit --no-fund
COPY panel/web/ ./
RUN npm run build

FROM golang:1.25-alpine AS build
WORKDIR /src
RUN apk add --no-cache git ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/dist ./panel/web/dist
ARG VERSION=1.0.0
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/panel-api ./panel/cmd/panel-api

FROM alpine:3.20.3
# postgresql16-client + openssl back the UI backup/restore endpoints (pg_dump /
# pg_restore against the PG16 server, matching the CLI's encryption).
RUN apk add --no-cache ca-certificates tzdata curl postgresql16-client openssl \
 && addgroup -g 10001 -S smartdns && adduser -u 10001 -S -G smartdns smartdns
COPY --from=build /out/panel-api /usr/local/bin/panel-api
# Named volumes inherit the ownership of the image directory they shadow.
RUN mkdir -p /var/lib/smartdns-panel && chown -R 10001:10001 /var/lib/smartdns-panel
VOLUME ["/var/lib/smartdns-panel"]
USER 10001:10001
EXPOSE 8080 8443
ENTRYPOINT ["/usr/local/bin/panel-api"]
