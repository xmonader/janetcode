BIN := janetcode
PKG := ./...

.PHONY: build test lint clean run

build:
	go build -o bin/$(BIN) ./cmd/$(BIN)

test:
	go test $(PKG)

lint:
	go vet $(PKG)

clean:
	rm -rf bin

run: build
	./bin/$(BIN)
