# Cross-compiling from the build platform, so a multi-arch build needs no
# emulated toolchain.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build

ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} GOARM=${TARGETVARIANT#v} \
    go build -trimpath -ldflags "-s -w" -o app ./cmd/app


# The binary reads protocol and table names from the iproute2 package. Alpine
# also keeps a shell and ip for debugging.
FROM alpine:3.24 AS release

RUN apk add --no-cache iproute2

WORKDIR /app

COPY --from=build /build/app .

CMD ["./app"]
