# syntax=docker/dockerfile:1

FROM golang:1.27-alpine AS build
ARG VERSION=0.1.0-dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/ioimg ./cmd/ioimg && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags "-s -w" -o /out/ioimgctl ./cmd/ioimgctl

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S ioimg && adduser -S -G ioimg ioimg && \
    mkdir -p /var/cache/ioimg && chown -R ioimg:ioimg /var/cache/ioimg
COPY --from=build /out/ioimg /usr/local/bin/ioimg
COPY --from=build /out/ioimgctl /usr/local/bin/ioimgctl
USER ioimg
ENV IOIMG_ADDR=:8080 \
    IOIMG_CACHE_DIR=/var/cache/ioimg \
    IOIMG_LOG_FORMAT=json
EXPOSE 8080
VOLUME ["/var/cache/ioimg"]
ENTRYPOINT ["/usr/local/bin/ioimg"]
