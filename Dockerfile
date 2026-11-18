# Cross-compiling from the build platform, so a multi-arch build needs no
# emulated toolchain.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:eb61c3873c97416fff0116553fff65d67a3549f72e11d9758849a689cdd8b523 AS build

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
FROM gcr.io/distroless/static:latest@sha256:1a64f46364620465ef9e9a8f5413757dfdf22695e050bbfd7303eeaf17a59383 AS release

COPY --from=iproute2 /usr/share/iproute2/rt_protos /usr/share/iproute2/rt_tables /usr/share/iproute2/

COPY --from=build /build/app /app

CMD ["/app"]
