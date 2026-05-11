BINARY := bunashimeji.exe
PKG    := ./...

.PHONY: all test build run clean

all: test build

test:
	go test $(PKG)

build:
	go build -o $(BINARY) .

run: build
	./$(BINARY) -name Anzu

clean:
	rm -f $(BINARY)
