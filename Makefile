VERSION ?= 0.1.0
LDFLAGS := -X main.version=$(VERSION)
BINARY  := kint-data

.PHONY: build test clean

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/kint-data/

test:
	go test ./...

clean:
	rm -f $(BINARY)
