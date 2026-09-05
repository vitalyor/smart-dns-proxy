# syntax=docker/dockerfile:1.7
# The subscription page is stateless: no database, no volumes, nothing to back up.

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=1.0.0
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/subscription-page ./subpage/cmd/subscription-page

FROM alpine:3.20.3
RUN apk add --no-cache ca-certificates tzdata curl \
 && addgroup -g 10001 -S smartdns && adduser -u 10001 -S -G smartdns smartdns
COPY --from=build /out/subscription-page /usr/local/bin/subscription-page
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/subscription-page"]
