APP := local-device-bridge

.PHONY: all build test vet fmt cross-build clean

all: test build

build:
	go build -trimpath -ldflags='-s -w' -o $(APP) ./cmd/local-device-bridge

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w cmd internal

cross-build:
	mkdir -p dist
	GOOS=darwin GOARCH=amd64 go build -trimpath -o dist/$(APP)-darwin-amd64 ./cmd/local-device-bridge
	GOOS=darwin GOARCH=arm64 go build -trimpath -o dist/$(APP)-darwin-arm64 ./cmd/local-device-bridge
	GOOS=linux GOARCH=amd64 go build -trimpath -o dist/$(APP)-linux-amd64 ./cmd/local-device-bridge
	GOOS=linux GOARCH=arm64 go build -trimpath -o dist/$(APP)-linux-arm64 ./cmd/local-device-bridge
	GOOS=windows GOARCH=amd64 go build -trimpath -o dist/$(APP)-windows-amd64.exe ./cmd/local-device-bridge

clean:
	rm -rf dist $(APP) $(APP).exe
