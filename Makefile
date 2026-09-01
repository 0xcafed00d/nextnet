.PHONY: test build build-mister clean

VERSION ?= dev
LDFLAGS := -X nextnet/internal/version.Version=$(VERSION)

test:
	go test ./...

build:
	mkdir -p dist
	go build -buildvcs=false -trimpath -ldflags="$(LDFLAGS)" -o dist/nextnet ./cmd/nextnet

build-mister:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 \
		go build -buildvcs=false -trimpath -ldflags="-s -w $(LDFLAGS)" \
		-o dist/nextnet-linux-armv7 ./cmd/nextnet

clean:
	rm -rf dist
