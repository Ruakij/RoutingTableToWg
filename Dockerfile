# Cross-compiling from the build platform, so a multi-arch build needs no
# emulated toolchain.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS build

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
FROM --platform=$BUILDPLATFORM alpine:3.24@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a AS iproute2

RUN apk add --no-cache iproute2


# Runs as root: configuring the Wireguard interface needs NET_ADMIN, which a
# container grants to root only (no ambient capabilities for other users).
FROM gcr.io/distroless/static:latest@sha256:448810357cc8e67b7208988abdba0f5ec3d3aebee0a5b13eb624979792cffc0f AS release

COPY --from=iproute2 /usr/share/iproute2/rt_protos /usr/share/iproute2/rt_tables /usr/share/iproute2/

COPY --from=build /build/app /app

CMD ["/app"]
