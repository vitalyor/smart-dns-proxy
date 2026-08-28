# syntax=docker/dockerfile:1.7
# One image with every node binary; Compose selects the entrypoint per service.

FROM golang:1.24.4-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=1.0.0
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/ \
    ./node/cmd/dns-frontend ./node/cmd/sni-proxy ./node/cmd/egress-relay \
 && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/node-agent ./agent/cmd/node-agent

FROM alpine:3.20.3
RUN apk add --no-cache ca-certificates tzdata bind-tools curl \
 && addgroup -g 10001 -S smartdns && adduser -u 10001 -S -G smartdns smartdns
COPY --from=build /out/ /usr/local/bin/
RUN mkdir -p /var/lib/smartdns-agent && chown -R 10001:10001 /var/lib/smartdns-agent
VOLUME ["/var/lib/smartdns-agent"]
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/node-agent"]
