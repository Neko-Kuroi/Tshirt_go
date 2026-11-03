# tshirt2-go

Tshirt2 在庫管理 (Flask 版) の Go 移植。**手順3 (認証と共通部品) まで**。

## 方針
- 同じ `inventory.db` をそのまま使う (スキーマ・`PRAGMA user_version` 方式は Flask 版と同一)
- SQLite は純 Go (`modernc.org/sqlite`)。cgo 不要
- SQLAlchemy 版で作られた DB の引き継ぎは**しない** (2026-11-01 決定)。検出したら書き込まずに止める

## 構成
```
internal/db/       手順1: 接続 (Open)、書き込み Tx (BEGIN IMMEDIATE)、Migrate、Check、migrations.go (生成物)
internal/util/     PyStrip / ToInt / SizeKey / FormatJST。Python と挙動が分かれやすい所を、Python の出力で照合
internal/repo/     読み取りクエリ (手順2では在庫操作・一覧・CSV が使う分だけ。画面用は手順4)
internal/service/  stock.go (入荷・出荷・調整・プリント変換) / stocklist.go (一覧) / export.go (CSV) / auth.go (ログイン認証・パスワード変更)
internal/auth/     パスワードの検証・生成 (Werkzeug 互換の scrypt / pbkdf2)、署名鍵の読み込み
internal/xcrypto/  x/crypto の scrypt と pbkdf2 のコピー (依存を増やさないため。README 参照)
internal/web/      HTTP の層: セッション・ログイン保持・CSRF・flash・権限・エラーページ・503 案内・静的ファイル
tools/             Flask 版から照合データを作るスクリプト (gen_migrations / gen_util_vectors / gen_golden / gen_hash_vectors / crosscheck_hash)
```


## 画面の追加のしかた (手順4以降)
`internal/web` は、画面に依存しない共通部品。各画面は、ルートを登録するだけでよい。
```go
s.Page("GET /blank", handler)        // ログイン必須。未ログインは /login?next=... へ (「ログインしてください。」を flash)
s.AdminPage("GET /admin/users", h)   // 管理者だけ。スタッフは 403、未ログインはログイン画面へ
s.Public("GET /login", h)            // ログイン不要
// handler は func(w, r) error。error を返すと、*HTTPError ならそのコード、DB が混み合っていれば 503、それ以外は 500 のページ
web.CurrentUser(r)                   // ログイン中のユーザー (Page / AdminPage の中では非 nil)
web.Flash(r, "success", "保存しました。")   // リダイレクトを挟んでも、次の画面に1回だけ出る
s.Render(w, r, 200, "inventory/xxx.html", data)  // templates/ 以下のページ。base.html の title / content ブロックを埋める
```
- POST などの状態を変える要求は、`csrf_token` (フォーム) か `X-CSRFToken` (ヘッダー) が必須。フォームには `{{template "csrf" .}}`
- templates/ に置くファイルは、`_` で始まる名前の部品も含めて埋め込まれる (`//go:embed all:templates`)
- `GET /{$}` のダッシュボードは、いまは仮の画面 (手順4で置き換える)

## 認証まわりの実測値 (Flask 版を実際に動かして確かめた挙動)
| 項目 | Flask 版 | Go 版 |
| --- | --- | --- |
| 通常のセッション | ブラウザを閉じるまで。サーバー側の上限は14日 | 同じ |
| 「ログインを保持する」 | 365日 (Flask-Login の既定) | 同じ (`RememberDuration`) |
| 未ログイン時の移動先 | `/login?next=<URLエンコードしたパスとクエリ>` + 「ログインしてください。」 | 同じ |
| 無効にしたユーザーの既存セッション | ログイン画面へ戻される | 同じ (remember Cookie も消す) |
| CSRF 失敗 | 400 + 決まった文言。トークンの有効期間は1時間 | 同じ (`CSRFTTL`) |
| パスワード変更 | 他の端末のセッションが無効になる | 同じ。変更した本人の remember Cookie は更新する |

## Flask 版との意図的な差分
- **切替時に、全員が1回ログインし直す** (Cookie の形式が違うため。ユーザー・パスワードは、そのまま使える)
- `next` の検証は、Python 版より厳しい (制御文字、`/\` で始まるもの。ブラウザが `\` を `/` として扱うため、Python 版では抜ける例がある)
- Cookie は、remember も含めて `SameSite=Lax`。`CookieSecure` を有効にすると、両方に `Secure` を付ける (Flask-Login の remember Cookie は、どちらも付かなかった)
- 「ログインを保持する」なしでログインしたときは、前に残っていた remember Cookie を消す (別のユーザーに戻らないように)
- ユーザーが存在しないときも、ダミーのハッシュで照合して、応答時間からユーザー名の有無を推測されないようにする
- ログイン成功時に、CSRF の乱数を作り直す (セッション固定の対策)
- 新しいパスワードは、Werkzeug 互換の scrypt (`scrypt:32768:8:1$...`) で保存する。Flask 版へ戻しても、そのままログインできる。古い pbkdf2 のハッシュは、自動では書き換えない
- DB が未作成/古いときは、POST でも CSRF エラー (400) より先に 503 の案内を返す
- メソッド違い (405) は、Go の標準の応答 (テキスト) になる。エラーページではない

## 照合の仕組み (Python の出力を正解にする)
手で期待値を書くと、移植する側の解釈が混ざる。そこで Flask 版のコードで正解データを作る。
- `tools/gen_util_vectors.py` → `internal/util/testdata/*.json`: `to_int` (全角数字・`1_000`・桁あふれ等)、`str.strip()`、`size_key` の並び (647件)
- `tools/gen_golden.py` → `internal/service/testdata/golden/`: Flask 版の services で作った DB (特殊文字・数式注入・
  `%` `_` を含む品番・特殊なサイズ・在庫0・停止中・sort_order が同じカテゴリー) と、その CSV (バイト列)、
  一覧の77条件ぶんの並び順・件数・合計、選択肢。Go は同じ DB から同じ出力を作り、一致を確かめる
- Flask 版が変わったら、3つのスクリプトを再実行して差分を見る (差分 = 移植漏れ)

## Python と Go で挙動が分かれる所 (照合で固定してある)
- `to_int`: Python の `int()` は、全角数字 (`１２`)、`1_000`、前後の空白 (U+001C〜1F を含む) を受け付ける。上付き `²` は受け付けない
- `size_key`: 数値は整数に変換せず「先頭の 0 を除いた長さ → 辞書順」で比べる (桁数無制限の Python と一致)
- CSV: Python の `csv.writer` は先頭の空白では引用符を付けない (Go の `encoding/csv` は付ける) ので、自前で書く
- 文字列の並び: Go のバイト順は UTF-8 ではコードポイント順と一致し、Python と同じになる

## 実行
```
go get modernc.org/sqlite
go mod tidy
go test ./...
```
- 最新の modernc.org/sqlite は go.mod で新しい Go (1.26) を要求する。手元の Go が古い場合は、
  `go get modernc.org/sqlite@<古めのバージョン>` で固定する。
- cgo 環境でドライバー差を切り分けるとき:
  `go get github.com/mattn/go-sqlite3 && go test -tags mattn ./...`
- Flask 版の schema.py が変わったら:
  `python tools/gen_migrations.py <Flask版>/inventory_app/app/schema.py` → 差分が出たら移植漏れ

## 使い方の要点
- 書き込みは必ず `d.Tx(ctx, func(conn *sql.Conn) error { ... })`。中では引数の `conn` だけを使う
  (`d.QueryContext` など別の接続を取る呼び出しは、自分が持つ書き込みロックを待つ恐れがある)
- 起動時は `db.Check(ctx, path)`:
  - `StateMissing`  → 初回セットアップ (init-db 相当)。ファイルは作られない
  - `StateOutdated` → `db.Migrate`
  - `StateForeign`  → `*db.ForeignSchemaError`。DB には書き込まず案内を出す
- `IsCurrent` は Flask 版と同じく `version >= len(Migrations)` (より新しい版も「最新」扱い)

## 検証状況 (2026-11-01)
- `-tags mattn` (cgo) で `go test -race -count=3` が合格 (16件)
- テストが壊れた実装を検出できることを確認済み (外部キー無効化、BEGIN IMMEDIATE→BEGIN、ロールバック省略、他版DB検出の削除)
- Flask 版との相互確認: Python が作った DB を Go が `current` と判定。Go が作った DB を Flask が
  `user_version=3`、`integrity_check=ok` で認識。`sqlite_master` の11オブジェクトが完全一致
- **modernc 経由は未実行** (この環境から依存を取得できない)。DSN の `_pragma=...` 指定と
  `database/sql` の複数文 Exec が前提。`go test ./...` の結果を確認してほしい

## Flask 版テストとの対応 (手順2時点)
| Flask 版 | 状況 |
| --- | --- |
| test_stock.py (14) | 全て移植 (サービス9、同時実行2、マイグレーション・制約3 は internal/db) |
| test_stock_list.py (30) | 21 を移植 (サービス層で成り立つ部分)。残りは手順3〜4: ログイン必須、見出しリンク、HTMLエスケープ、画面間リンク、マトリクスのタイプ表示、管理画面の品番登録 (6) |
| test_export.py (13) | 11 を移植。残りは手順4: ログイン必須、ダウンロードのヘッダー (Content-Type / Disposition / Cache-Control) |
| test_robustness.py (9) | サービス層の3 (巨大な数量・上限・variant が残らない) を移植。DB未作成/古いDB は `db.Check` で対応済み。残りは手順3〜5: 巨大な数値のHTTP応答、init-db、セッション Cookie |
| test_utils.py (7) | 単体6 を移植 (+ Python との照合)。結合1 は手順4 (画面) |
| test_sqlalchemy_db_and_review.py (18) | SQLAlchemy 引き継ぎ5 は対象外 (決定)。残りは手順3〜4 |
| test_web.py (11) | ログイン必須・ログアウトと open redirect 対策・スタッフの管理者ページ拒否・無効ユーザー は手順3で移植 (+ 認証・CSRF・Cookie・flash・エラー・503 の新規テスト)。画面ごとの描画は手順4 |
| test_cli.py (4), test_create_user.py (3) | 手順5 (サブコマンド) |

## 検証状況 (2026-11-01)
- `-tags mattn` (cgo) で `go test ./...` が合格 (140件: db 18 / util 17 / repo 1 / service 55 / auth 4 / web 45)。`-race` でも合格 (scrypt が遅くなるので web は約90秒)
- テストが壊れた実装を検出できることを確認済み (変異を入れて落ちるかを確認: util 5種、service 11種、db 4種、web・認証 23種)
- Flask が作った DB (Werkzeug の scrypt ハッシュ入り) に、Go のサーバーでログインできることを確認 (`interop_test.go`)。
  逆に、Go が作ったハッシュを Werkzeug の `check_password_hash` が受け付けることも確認 (`tools/crosscheck_hash.py`)
- **modernc 経由は未実行** (この環境から依存を取得できない)

## 手順4 (画面) — 2026-11-03 追記

- 全16画面を移植し、`routes()` へ配線した (読み取り系: ダッシュボード、無地在庫マトリクス、
  一覧、プリント済み / 書き込み系: 入荷、変換、出荷・調整、履歴、備考、CSV 出力 / 管理: 品番、
  カテゴリー、ユーザー。`CreateUser` は `internal/service/admin.go`)
- テストは 169件 (db 18 / util 17 / repo 1 / service 60 / auth 5 / web 68)。
  modernc (純 Go) と mattn (cgo) の両方で合格。ただし次項の1件を除く
- **modernc は実行確認済み** (Go 1.27.3 の環境で `modernc.org/sqlite v1.61.1` を取得し、
  全テストを実行)。手順1〜3の「modernc 未実行」の注記は解消した
- 同じ DB (Flask 版が作成し、Werkzeug ハッシュのユーザーを含む) で両実装を動かし、
  主要15画面の表示テキストが完全一致することを確認した。Go 版へのログインも Flask 版が
  作ったハッシュで通る
- `/static/../go.mod` のようなパスは、ServeMux の正規化リダイレクト (Go の版で
  301/307 が変わる) に頼らず、`rejectDotSegments` が手前でアプリの 404 を返す
  (Flask 版と同じ見え方。Go 1.27 で `TestStaticFilesETagAndNotModified` が落ちた
  ことを受けて導入し、全169件が両ドライバーで合格するようになった)
- 表示上の差分 (ブラウザでは同一): `html/template` が `+` を `&#43;` にエスケープする、
  備考の `back` に末尾 `?` が付かない

## 手順5 (CLI・ビルド) — 2026-11-03 追記

- 単一バイナリ `inventory` が完成。引数なし (または `serve`) でサーバー、
  サブコマンドで CLI: `init-db` / `seed` / `create-user` / `list-users` / `help`
  (実装は `internal/cli`、入口は `cmd/inventory`)。文言・終了コードは Flask 版の
  CLI に合わせ、案内文のコマンド名だけ `inventory` に置き換えた
- 設定は Flask 版 config.py と同じ規則: DB は `INVENTORY_DATABASE` →
  `<バイナリの場所>/instance/inventory.db`、秘密鍵は `INVENTORY_SECRET_KEY` →
  `<バイナリの場所>/instance/secret_key`。`INVENTORY_COOKIE_SECURE=1`、
  待受は `--addr` / `INVENTORY_ADDR` (既定 127.0.0.1:5000)
- テストは 187件 (cli 18件を追加)。両ドライバーで合格 (下記の注記を除く)
- ビルドは Makefile (`make build` = 純 Go、検証用に `make build-mattn`)。
  サイズ実測 (Go 1.27.3、-trimpath): 純 Go は素で 19.9 MiB、`-s -w` で 13.5 MiB
  (静的リンク、単体で動作)。mattn 版は 11.1 MiB だが glibc が要る。
  手順0の試作より大きいのは modernc 自体の大きさによる。UPX を使えば
  さらに圧縮できる (手順0の実績では約1/3)
- 書き込みトランザクション (`db.Tx`) は、SQLITE_BUSY のときに指数バックオフで
  全体を再試行する (50ms から倍々に最大1秒、ジッター付き、最大5回)。
  1回あたりのロック待ちは busy_timeout (5000ms、Flask 版 Python sqlite3 の既定と
  同じ) が吸収し、リトライは「5秒待っても駄目だった」ときの保険。別プロセスが
  同じ DB を書いている間 (手順6の並行稼働、稼働中の CLI 実行) でも書き込みが
  通るようにするためのもので、低速な検証環境で同時書き込みテストがまれに
  落ちたことを受けて入れた。再試行の単体テストと、ロックを5秒超保持する
  統合テストあり (internal/db/tx_retry_test.go)

## 手順6 (並行稼働と切替) — 2026-11-03 追記

同じ inventory.db に対して Flask 版と Go 版を同時起動し、相互運用を実経路で確認した (17項目、全て合格):

- 両方とも、Go 版 CLI が作ったユーザーでログインできる
- Flask 版の入荷が Go 版の在庫・履歴に、Go 版の出荷が Flask 版の在庫・履歴に、それぞれ即座に反映される
- Go 版の管理画面で変更したパスワードで、Flask 版にログインできる (旧パスワードは拒否される)
- Go 版で追加した備考が Flask 版に表示され、Flask 版で解決すると Go 版の未解決一覧から消える
- 両サーバーへの交互の並行入荷 (10件) が全て成功し、最終在庫数が一致する
- 検証後の DB: user_version=3 のまま、integrity_check=ok、外部キー違反なし、
  スキーマ (11オブジェクト) は Flask 版が作った素の DB と完全一致

## 切替手順 (Flask 版 → Go 版)

前提: Go 版は Flask 版と同じ instance フォルダ (inventory.db と secret_key) を
そのまま使う。環境変数 (INVENTORY_DATABASE / INVENTORY_SECRET_KEY /
INVENTORY_COOKIE_SECURE) の意味も Flask 版と同じ。
INVENTORY_BEHIND_PROXY は Go 版では不要 (絶対 URL を生成しないため)。

1. Flask 版 (gunicorn / waitress) を止める
2. バイナリ `inventory` を任意の場所に置く (DB の既定の場所はバイナリの隣の
   instance/ なので、Flask 版の instance を使う場合は INVENTORY_DATABASE で
   既存の inventory.db を指すか、バイナリを Flask 版と同じフォルダに置く)
3. `./inventory init-db` を実行する (冪等。スキーマが最新なら何も変わらない)
4. 起動する。tailscale serve の転送先に合わせ、Flask 版と同じポートで受ける:
   `INVENTORY_COOKIE_SECURE=1 ./inventory serve --addr 127.0.0.1:8000`
5. ログイン画面 → ログイン → ダッシュボード表示を確認する

注意 (利用者向け): セッション Cookie の形式が違うため、切替時に全員が
1回だけログインし直す。ユーザー名とパスワードはそのまま使える。

## 戻し方 (Go 版 → Flask 版)

Go 版を止めて Flask 版を起動するだけ。DB の形式・スキーマ・user_version は
Go 版が変更しないため、そのまま戻せる。並行稼働も可能 (上記のとおり検証済み)
なので、片方を試してから切り替えることもできる。
