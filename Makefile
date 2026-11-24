VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build build-linux test clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/boxhand-agent$(if $(filter Windows_NT,$(OS)),.exe,) ./cmd/boxhand-agent

build-linux:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/boxhand-agent-linux-amd64 ./cmd/boxhand-agent
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/boxhand-agent-linux-arm64 ./cmd/boxhand-agent

test:
	go vet ./...
	go test ./...

clean:
	rm -rf bin
