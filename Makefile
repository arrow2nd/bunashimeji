BINARY := bunashimeji.exe
PKG    := ./...

# git describe をバージョン文字列として埋め込む。tag が無い場合は短いハッシュにフォールバック。
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# -H=windowsgui で GUI subsystem 扱いになり、exe 起動時のコンソール窓を抑制する。
# log.Printf の出力先 (stderr) は消えるので、ログを見たい場合は build-console を使う。
LDFLAGS := -H=windowsgui -X main.version=$(VERSION)

.PHONY: all test build build-console run dist clean

all: test build

test:
	go test $(PKG)

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

# 開発・デバッグ用: コンソールを残したいときはこちら (log.Printf がターミナルに出る)
build-console:
	go build -ldflags "-X main.version=$(VERSION)" -o $(BINARY) .

run: build-console
	./$(BINARY) -name Anzu

# 配布用 zip を dist/ に作る。GOOS=windows でクロスコンパイルする。
dist:
	VERSION=$(VERSION) ./scripts/dist.sh

clean:
	rm -f $(BINARY)
	rm -rf dist
