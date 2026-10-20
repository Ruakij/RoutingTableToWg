LDFLAGS = -s -w

.PHONY: build
build:
	CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "$(LDFLAGS)" -o bin/routingtabletowg ./cmd/app
