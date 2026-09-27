PREFIX ?= $(HOME)/.local
BIN := bin/aitodo

.PHONY: build install test clean

build:
	go build -o $(BIN) .

install: build
	install -d $(PREFIX)/bin
	install -m 755 $(BIN) $(PREFIX)/bin/aitodo

test:
	go test ./...

clean:
	rm -rf bin
