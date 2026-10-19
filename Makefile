.PHONY: build test vet check spec clean

build:
	mkdir -p bin
	go build -trimpath -o bin/snapchat-mcp .

test:
	go test -race ./...
	node --test internal/service/*.test.mjs
	node bridge.test.cjs

vet:
	go vet ./...

check: vet test

spec:
	go run . api-spec > docs/openapi.json

clean:
	rm -rf bin
