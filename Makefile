.PHONY: test build build-linux-amd64 build-linux-arm64 install-macos uninstall-macos

test:
	go test -v -short ./...

build:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o ./porter ./cmd/porter

build-linux-amd64:
	@mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o dist/porter-linux-amd64 ./cmd/porter

build-linux-arm64:
	@mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o dist/porter-linux-arm64 ./cmd/porter

install-macos:
	@bash packaging/macos/install.sh

uninstall-macos:
	@bash packaging/macos/uninstall.sh
