.PHONY: test build build-mister package-mister clean

VERSION ?= dev
LDFLAGS := -X nextnet/internal/version.Version=$(VERSION)

test:
	go test ./...
	sh scripts/install_test.sh

build:
	mkdir -p dist
	go build -buildvcs=false -trimpath -ldflags="$(LDFLAGS)" -o dist/nextnet ./cmd/nextnet

build-mister:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 \
		go build -buildvcs=false -trimpath -ldflags="-s -w $(LDFLAGS)" \
		-o dist/nextnet-linux-armv7 ./cmd/nextnet

package-mister: build-mister
	mkdir -p dist/nextnet-mister
	cp dist/nextnet-linux-armv7 dist/nextnet-mister/nextnet
	cp scripts/install.sh scripts/uninstall.sh dist/nextnet-mister/
	chmod 755 dist/nextnet-mister/nextnet dist/nextnet-mister/install.sh dist/nextnet-mister/uninstall.sh
	tar -C dist -czf dist/nextnet-mister-armv7.tar.gz nextnet-mister
	cd dist && sha256sum nextnet-mister-armv7.tar.gz > nextnet-mister-armv7.tar.gz.sha256

clean:
	rm -rf dist
