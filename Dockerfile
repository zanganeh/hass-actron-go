ARG BUILD_FROM
ARG TARGETARCH
ARG TARGETVARIANT

# Build stage — compiles for the target arch
FROM --platform=${BUILDPLATFORM} golang:1.22-alpine AS builder

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build static binary for the target platform
ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} GOARM=${TARGETVARIANT#v} \
    CGO_ENABLED=0 \
    go build -ldflags="-s -w" -trimpath \
    -o hass-actron ./cmd/hass-actron

# Final stage — minimal image with the binary
FROM ${BUILD_FROM}

COPY --from=builder /build/hass-actron /usr/bin/hass-actron

EXPOSE 180

CMD ["/usr/bin/hass-actron"]
