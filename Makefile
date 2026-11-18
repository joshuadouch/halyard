VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
GOFLAGS := -trimpath

.PHONY: all web build release test lint clean

all: build

web:
	cd web && npm ci && npm run build

build: web
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o bin/halyard ./cmd/halyard

release: web
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o dist/halyard-linux-amd64 ./cmd/halyard
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o dist/halyard-linux-arm64 ./cmd/halyard
	cd dist && sha256sum halyard-linux-* > SHA256SUMS

test:
	go vet ./...
	go test ./...

lint:
	gofmt -l . | (! grep .)
	bash -n ct/halyard.sh install/halyard-install.sh
	cd web && npx tsc --noEmit

clean:
	rm -rf bin dist
	find internal/web/dist -mindepth 1 ! -name .keep -delete
