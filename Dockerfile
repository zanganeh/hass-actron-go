ARG BUILD_FROM=alpine:3.19
ARG BUILDPLATFORM=linux/amd64

# Build stage — always runs on native (amd64); cross-compiles via GOOS/GOARCH
FROM --platform=${BUILDPLATFORM} golang:1.22-alpine AS builder

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETARCH=amd64
ARG TARGETVARIANT=""
RUN GOOS=linux GOARCH=${TARGETARCH} GOARM=${TARGETVARIANT} \
    CGO_ENABLED=0 \
    go build -ldflags="-s -w" -trimpath \
    -o hass-actron ./cmd/hass-actron

# Final stage — minimal image with the binary
FROM ${BUILD_FROM}

COPY --from=builder /build/hass-actron /usr/bin/hass-actron

EXPOSE 180

CMD ["/usr/bin/hass-actron"]
