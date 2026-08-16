ARG GO_VERSION=1.24
ARG VERSION=dev
FROM golang:${GO_VERSION} AS builder

WORKDIR /app

COPY go.mod go.sum /app/
RUN go mod download

COPY . /app/
# ARG before the first FROM is only visible to FROM lines; re-declare so the
# build below can use it.
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -o app ./cmd/v6pool

# Alpine runtime: provides a real `ip` binary so claim mode (tether / routed
# prefixes) works inside the container, plus CA certificates for TLS to
# upstream sites. A static binary from the builder runs unmodified.
FROM alpine:3.20

RUN apk add --no-cache iproute2 ca-certificates && mkdir -p /etc/v6pool

WORKDIR /app

COPY --from=builder /app/app /app/
COPY docker/entrypoint.sh /docker/entrypoint.sh
RUN chmod +x /docker/entrypoint.sh

ENTRYPOINT ["/docker/entrypoint.sh"]
