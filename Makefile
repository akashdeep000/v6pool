BIN     := v6pool
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
GOBIN   ?= $(shell go env GOPATH)/bin
GOLANGCI ?= $(GOBIN)/golangci-lint
GORELEASER ?= $(GOBIN)/goreleaser

.PHONY: all build test vet fmt lint install release clean

all: build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN) ./cmd/v6pool

test:
	go test ./...

vet:
	go vet ./...

fmt:
	@files=$$(gofmt -s -l .); \
	if [ -n "$$files" ]; then \
		echo "gofmt needed on:"; echo "$$files"; gofmt -s -w .; \
	else \
		echo "gofmt: clean"; \
	fi

lint:
	$(GOLANGCI) run

release:
	$(GORELEASER) --snapshot --clean

install: build
	sudo install -m 0755 $(BIN) /usr/local/bin/v6pool
	sudo mkdir -p /etc/v6pool
	@if [ ! -f /etc/v6pool/config.yaml ]; then \
		sudo cp config.example.yaml /etc/v6pool/config.yaml; \
		echo "note: edit /etc/v6pool/config.yaml (set passwords!)"; \
	fi
	sudo cp v6pool.service /etc/systemd/system/
	sudo systemctl daemon-reload
	sudo systemctl enable --now v6pool
	sudo systemctl status v6pool --no-pager

clean:
	rm -f $(BIN)
