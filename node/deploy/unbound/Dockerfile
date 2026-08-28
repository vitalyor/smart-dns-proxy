# syntax=docker/dockerfile:1.7
FROM alpine:3.20.3
RUN apk add --no-cache unbound drill ca-certificates \
 && mkdir -p /var/lib/unbound /etc/unbound \
 && (unbound-anchor -a /var/lib/unbound/root.key || true) \
 && chown -R unbound:unbound /var/lib/unbound
RUN mkdir -p /etc/unbound/unbound.conf.d
COPY node/deploy/unbound/unbound.conf /etc/unbound/unbound.conf
EXPOSE 53/udp 53/tcp
USER unbound
ENTRYPOINT ["unbound", "-d", "-c", "/etc/unbound/unbound.conf"]
