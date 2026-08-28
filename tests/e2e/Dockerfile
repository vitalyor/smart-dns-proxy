# syntax=docker/dockerfile:1.7
# Test doubles for the container lab. Never deployed to production.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/ ./tests/e2e/mockorigin ./tests/e2e/mockauth

FROM alpine:3.20.3
RUN apk add --no-cache ca-certificates bind-tools curl openssl
COPY --from=build /out/ /usr/local/bin/
