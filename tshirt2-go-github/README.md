# tshirt2-go — 在庫管理アプリ Go 版

Tシャツ・キャップ等の無地在庫とプリント済み在庫を管理する Web アプリの Go 移植版です。
Flask 版 (Python) と **同じ `inventory.db` をそのまま読み書きでき**、画面・業務規則・文言も Flask 版に一致させています。配布物は単一バイナリ1つです。

- 設計の考え方と実装の詳細は [DESIGN.md](DESIGN.md)
- 移植作業の経過記録は [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md)

## 特徴

- **単一バイナリ**：純 Go (CGO 不要、静的リンク) でビルドでき、バイナリ1つで動作します
- **Flask 版と DB 共用**：スキーマ・`PRAGMA user_version`・パスワードハッシュ (Werkzeug 互換 scrypt/pbkdf2) まで互換。両実装の並行稼働と、いつでも Flask 版へ戻すことが可能 (検証済み)
- **依存は最小限**：標準ライブラリ (net/http、html/template) ＋ SQLite ドライバー。静的ファイル (Bootstrap 含む) はバイナリに埋め込み

## 必要なもの

- ビルド：Go 1.26 以降 (go.mod の要求に合わせる)
- 実行：なし (単一バイナリのみ)

## ビルド

```sh
make build        # → dist/inventory (純 Go、-trimpath -ldflags "-s -w")
make test         # テスト (純 Go ドライバー)
make test-mattn   # テスト (CGO ドライバー、検証用)
```

Makefile を使わない場合は `CGO_ENABLED=0 go build -o inventory ./cmd/inventory` でも構いません。

## 使い方

```sh
./inventory init-db                  # DB の作成・更新 (何度実行しても安全)
./inventory seed                     # 初期カテゴリー (Tシャツ/キャップ/カバン/バッジ) を登録
./inventory create-user admin --admin  # ユーザー作成 (パスワードは対話入力。--password 指定も可)
./inventory list-users               # ユーザー一覧
./inventory serve --addr 127.0.0.1:5000  # サーバー起動 (引数なしでも同じ)
```

初回は `init-db` → `seed` → `create-user` の順に実行し、`serve` で起動します。
DB が未作成・旧版のまま起動しても、画面に 503 の案内が出て、`init-db` を実行すれば再起動なしで復旧します。

## 設定

Flask 版と同名・同義の環境変数を使います。

| 環境変数 | 意味 | 既定値 |
| --- | --- | --- |
| `INVENTORY_DATABASE` | SQLite ファイルのパス | `<バイナリの場所>/instance/inventory.db` |
| `INVENTORY_SECRET_KEY` | Cookie 署名鍵 | 未設定なら `<バイナリの場所>/instance/secret_key` を自動生成 (0600) |
| `INVENTORY_COOKIE_SECURE` | `1` で Cookie に Secure 属性 (HTTPS 公開時) | 無効 |
| `INVENTORY_ADDR` | 待受アドレス (`--addr` フラグが優先) | `127.0.0.1:5000` |

本番では Flask 版と同様に `127.0.0.1` で待ち受け、`tailscale serve` 等で HTTPS 化する構成を想定しています。

## Flask 版からの切替

1. Flask 版を止める
2. バイナリを置き、`inventory init-db` を実行 (冪等)
3. Flask 版と同じポートで `inventory serve` を起動

利用者は **1回だけログインし直し** が必要です (セッション Cookie の形式が異なるため)。ユーザー名・パスワードはそのまま使えます。
戻すときは Go 版を止めて Flask 版を起動するだけです。DB は無変更のため、そのまま戻せます。

## テスト

```sh
go test ./...                 # 純 Go (modernc.org/sqlite)
go test -tags mattn ./...     # CGO (mattn/go-sqlite3) でも同じテストが通る
```

192件。Flask 版のテスト151件を正本として移植し、期待値の多くは Flask 版のコードから生成した照合データ (ベクトル・ゴールデン DB/CSV) で固定しています。詳しくは DESIGN.md の「テスト戦略」。

## 構成

```
cmd/inventory/     エントリーポイント (薄い main)
internal/cli/      サブコマンド (serve / init-db / seed / create-user / list-users)
internal/web/      HTTP 層 (セッション・CSRF・画面ハンドラー・テンプレート・静的ファイル)
internal/service/  業務規則 (入出荷・変換・一覧・CSV・管理操作・認証)
internal/repo/     読み取りクエリ
internal/db/       接続・書き込みトランザクション・マイグレーション
internal/auth/     パスワード照合・生成 (Werkzeug 互換)、署名鍵
internal/util/     Python 互換の小さな部品 (to_int、strip、size_key、JST 表示)
internal/xcrypto/  scrypt / pbkdf2 (依存を増やさないための取り込み)
tools/             Flask 版から照合データを生成するスクリプト
```
