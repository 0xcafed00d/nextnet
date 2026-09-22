.PHONY: test version build build-mister package-mister release-zip clean

BUILD_VERSION := $(shell sh scripts/version.sh)
LDFLAGS := -X nextnet/internal/version.Version=$(BUILD_VERSION)

test:
	go test ./...
	sh scripts/install_test.sh
	sh scripts/version_test.sh
	sh scripts/release_test.sh

version:
	@printf '%s\n' "$(BUILD_VERSION)"

build:
	mkdir -p dist
	go build -buildvcs=false -trimpath -ldflags="$(LDFLAGS)" -o dist/nextnet ./cmd/nextnet

build-mister:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 \
		go build -buildvcs=false -trimpath -ldflags="-s -w $(LDFLAGS)" \
		-o dist/nextnet-linux-armv7 ./cmd/nextnet

package-mister: build-mister
	mkdir -p dist/nextnet-mister/examples
	cp dist/nextnet-linux-armv7 dist/nextnet-mister/nextnet
	cp scripts/install.sh scripts/uninstall.sh dist/nextnet-mister/
	cp README.md dist/nextnet-mister/
	cp examples/esp-reset.bas dist/nextnet-mister/examples/
	chmod 755 dist/nextnet-mister/nextnet dist/nextnet-mister/install.sh dist/nextnet-mister/uninstall.sh
	chmod 644 dist/nextnet-mister/README.md dist/nextnet-mister/examples/esp-reset.bas
	tar -C dist -czf dist/nextnet-mister-armv7.tar.gz nextnet-mister
	cd dist && sha256sum nextnet-mister-armv7.tar.gz > nextnet-mister-armv7.tar.gz.sha256

release-zip:
	./scripts/build-release.sh

clean:
	rm -rf dist
