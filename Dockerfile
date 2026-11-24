# Cross-compiling from the build platform, so a multi-arch build needs no
# emulated toolchain.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS build

ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} GOARM=${TARGETVARIANT#v} \
    go build -trimpath -ldflags "-s -w" -o app ./cmd/app


# The binary reads protocol and table names from the iproute2 package. The
# files are plain text, so the build platform's package serves every target and
# needs no emulation.
FROM --platform=$BUILDPLATFORM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf AS iproute2

RUN apk add --no-cache iproute2


# Runs as root: configuring the Wireguard interface needs NET_ADMIN, which a
# container grants to root only (no ambient capabilities for other users).
FROM gcr.io/distroless/static:latest@sha256:4c2cd1dee442c9a99924a378c7c126fac8fef14771579ef567c3f2bdd7a5122a AS release

COPY --from=iproute2 /usr/share/iproute2/rt_protos /usr/share/iproute2/rt_tables /usr/share/iproute2/

COPY --from=build /build/app /app

CMD ["/app"]
