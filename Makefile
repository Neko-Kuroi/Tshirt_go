# tshirt2 (在庫管理アプリ Go 版) のビルド。
# 既定は純 Go (modernc.org/sqlite)。配布物は単一バイナリ dist/inventory。
GO ?= go
DIST := dist
BIN := $(DIST)/inventory

.PHONY: build build-mattn test test-mattn vet clean

# 配布用ビルド: CGO 無効 (純 Go)、デバッグ情報を落として小さくする
build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w" -o $(BIN) ./cmd/inventory

# 検証用ビルド: mattn/go-sqlite3 (CGO) 版
build-mattn:
	CGO_ENABLED=1 $(GO) build -tags mattn -trimpath -ldflags "-s -w" -o $(DIST)/inventory-mattn ./cmd/inventory

test:
	$(GO) test ./... -count=1

test-mattn:
	CGO_ENABLED=1 $(GO) test -tags mattn ./... -count=1

vet:
	$(GO) vet ./...

clean:
	rm -rf $(DIST)
