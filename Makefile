BINARY := bunashimeji.exe
PKG    := ./...

# -H=windowsgui で GUI subsystem 扱いになり、exe 起動時のコンソール窓を抑制する。
# log.Printf の出力先 (stderr) は消えるので、ログを見たい場合は build-console を使う。
LDFLAGS := -H=windowsgui

.PHONY: all test build build-console run clean

all: test build

test:
	go test $(PKG)

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

# 開発・デバッグ用: コンソールを残したいときはこちら (log.Printf がターミナルに出る)
build-console:
	go build -o $(BINARY) .

run: build-console
	./$(BINARY) -name Anzu

clean:
	rm -f $(BINARY)
