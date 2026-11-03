# DESIGN — Go 版のデザインスキーム

Flask 版在庫管理アプリを Go へ移植する際の設計方針と、各層の実装判断をまとめた文書です。
対象読者は、このコードを読む人・保守する人・Flask 版と突き合わせて検証する人です。

## 1. 設計原則

1. **Flask 版が仕様**。画面・文言・業務規則・計算結果は Flask 版に一致させる。判断に迷ったら Flask 版の実装と実測挙動を正とする。
2. **DB は無変更で共用**。スキーマ、`PRAGMA user_version` 方式、保存される値の形式 (パスワードハッシュ、日時の UTC 文字列など) を一切変えない。いつでも Flask 版へ戻せることが、切替の安全条件。
3. **期待値は生成して固定する**。移植者が手で書いた期待値は解釈違いを混ぜるため、照合データは Flask 版のコードから生成する (6章)。
4. **依存は最小限**。HTTP とテンプレートは標準ライブラリ、追加依存は SQLite ドライバーだけ。単一バイナリ・静的リンクで配布できることを優先する。
5. **意図的な差分は明記する**。Flask 版と変えた点は理由とともに 7章に列挙し、コード側のコメントにも残す。

## 2. 全体構成

依存は上位 → 下位の一方向。HTTP の都合が業務規則に漏れないようにし、業務規則は `*sql.Conn` (トランザクション内の接続) だけを受け取る形にしてある。

```
cmd/inventory   薄い main。baseDir (バイナリの場所) を決めて cli.Run を呼ぶだけ
internal/cli    サブコマンド。引数・環境変数・標準入出力の処理と、サーバー起動
internal/web    HTTP 層。ルーティング、認証ガード、セッション/CSRF/flash、描画
internal/service 業務規則。在庫操作・一覧・CSV・管理操作・ログイン認証 (純粋な入出力)
internal/repo   読み取りクエリ (画面・一覧・CSV が使う SELECT 群)
internal/db     接続プール、書き込みトランザクション、マイグレーション、状態判定
internal/auth   パスワードの照合・生成、署名鍵の読み込み (Flask の werkzeug/config 相当)
internal/util   Python 互換の小部品 (to_int、strip、size_key、JST 表示、安全な next)
internal/xcrypto scrypt / pbkdf2。golang.org/x/crypto を依存に足さず、必要部分だけ取り込んだもの
tools/          Flask 版から照合データを生成する Python スクリプト群
```

## 3. データ層 (internal/db)

### スキーマとマイグレーション
- マイグレーション SQL は Flask 版 `schema.py` から `tools/gen_migrations.py` で機械生成し、`migrations.go` に埋め込む。手で書き写さない (Flask 版が変わったら再生成し、差分が出れば移植漏れとして扱う)。
- 起動時の状態判定 `Check` は4状態：missing (ファイルなし) / outdated / current / foreign。foreign は SQLAlchemy 版など他系統の DB を指し、書き込まずに案内を出して止める (引き継ぎはしないという決定事項)。
- `IsCurrent` は Flask 版と同様 `version >= 最新` (新しい版も「最新」扱い)。

### トランザクション
- 書き込みは `db.Tx` に一本化。`BEGIN IMMEDIATE` で開始し、関数がエラーを返せば全てロールバックする (Flask 版 `transaction()` と同じ意味論)。
- 中では渡された `*sql.Conn` だけを使う。プールから別接続を取ると、自分の書き込みロックを自分で待つ形になりうるため、規約としてコメントとレビューで担保する。
- 接続には DSN で `foreign_keys=ON`、`journal_mode=WAL`、`busy_timeout=5000` を接続ごとに指定する (PRAGMA は接続単位の設定なので、プールを使う以上 DSN 指定が必須)。

### SQLITE_BUSY の指数バックオフ・リトライ
`Tx` は BUSY/LOCKED のとき、トランザクション全体を再試行する。

- 50ms から倍々に (上限1秒)、ジッター付き、最大5回。失敗した試行はロールバック済みなので再実行は安全。
- 1回あたりの待ちは busy_timeout (5秒、Python sqlite3 の既定と同じ) が吸収し、リトライはその先の保険。最悪でも約32秒で打ち止めになり、エラーとして返る。リクエストのキャンセルでも即座に中断する。
- 目的は、別プロセスが同じ DB を書いている間 (Flask 版との並行稼働、稼働中の CLI 実行) に書き込みを落とさないこと。
- BUSY 判定はドライバーごとに実装する (`driver_modernc.go` / `driver_mattn.go` の `isBusyError`)。エラー型と拡張コードの扱いがドライバーで異なるため。

### ドライバーの切り替え
- 既定は純 Go の `modernc.org/sqlite` (CGO 不要、静的リンク可能)。
- `-tags mattn` で `mattn/go-sqlite3` (CGO) に差し替えられる。差分はドライバーファイル2つだけ。枯れた C 実装と同じテストを通すことで純 Go ドライバーの挙動を裏取りし、問題時の切り分け・逃げ道にもする。配布は純 Go 版のみ。

## 4. ドメイン層 (internal/service, internal/repo)

- 在庫操作 (入荷・出荷・調整・プリント変換) は `stock.go`。複数行の更新は `batch_id` でひとかたまりにし、途中失敗で部分的に残らない (在庫が負数になる操作はサービス層で弾く)。
- 一覧・マトリクスは `stocklist.go` と web 側の `matrix.go`。並び順はカテゴリーの `sort_order` → 色 → サイズ (size_key) → 種類名。
- CSV 出力は `export.go`。Python の `csv.writer` と引用符の付け方が違うため `encoding/csv` は使わず自前で書く (先頭空白では引用符を付けない、BOM・改行コードまで Flask 版に一致させ、バイト列で照合)。
- 管理操作 (カテゴリー・品番・ユーザー) は `admin.go`。画面側の検証規則 (パスワード最小長など) はここに置き、CLI の create-user は規則が違うため流用しない (CLI は Flask 版 CLI と同じく長さ規則なし)。

### Python と Go で分かれる挙動の吸収 (internal/util)
実測とベクトル照合で固定した箇所：
- `ToInt`：Python の `int()` は全角数字、`1_000` の区切り、前後の各種空白を受け付ける。再現しないとフォーム入力の受理範囲が変わる。
- `PyStrip`：Python の `str.strip()` の対象文字集合 (U+3000 等を含む) を再現。ユーザー名の同一性判定に関わる。
- `SizeKey`：Python は任意精度整数。Go では数値化せず「先頭ゼロを除いた (長さ, 辞書順)」で比較し、桁数無制限のまま一致させる。
- 文字列順序：UTF-8 のバイト順はコードポイント順と一致する性質を利用し、Python の並びと揃える。

## 5. 認証 (internal/auth, internal/web, service/auth.go)

### パスワードハッシュ
- Werkzeug 形式 (`scrypt:32768:8:1$<salt>$<hash>` / `pbkdf2:sha256:<反復>$<salt>$<hash>`) を読み書きできることが互換の要。検証は Flask 版が生成したハッシュベクトル (日本語パスワード含む) で照合済み。
- 細部：salt は文字列のバイト列そのまま、pbkdf2 の反復回数はハッシュ文字列に書かれた値を読む。規格化 (NFKC) はしない (Werkzeug の実装に合わせる)。
- 新規作成・変更時は scrypt で保存する。Flask 版へ戻してもそのままログインでき、Go 版で作ったハッシュを Werkzeug が検証できることも照合済み。旧 pbkdf2 ハッシュは自動では書き換えない。
- メモリ：scrypt は1回で約34MB の作業領域を使う。並行ログインで RSS が跳ね上がり、待機中は GC が走らず解放済みの領域が回収されないまま残る問題があったため、internal/auth の派生処理は同時実行を最大4に絞り、派生のたびに `debug.FreeOSMemory()` で作業領域を OS に返している (負荷後の RSS は約18MB で一定)。

### セッション
サーバー側にセッションを持たず、署名付き Cookie に寄せる (Flask 版と同じ発想)。

- セッション Cookie：署名付きで、ユーザー ID と `session_token` を保持。`session_token` はパスワードハッシュから導く値で、パスワード変更・無効化で全セッションが失効する (Flask-Login + Flask 版の実装と同じ効果)。
- 有効期間：通常セッションはブラウザを閉じるまで (サーバー側の上限14日)。「ログインを保持する」は remember Cookie で365日。
- 無効化されたユーザーの既存セッションは次回リクエストでログイン画面へ戻し、remember Cookie も消す。
- ユーザー名が存在しない場合もダミーハッシュで照合し、応答時間からユーザー有無を推測させない。ログイン成功時に CSRF の乱数を作り直す (セッション固定対策)。

### CSRF と flash
- CSRF トークンはセッションに結び、有効期間1時間 (Flask-WTF の既定と同じ)。フォームの `csrf_token` か `X-CSRFToken` ヘッダーで受ける。
- flash は署名付き Cookie に1回分だけ載せ、表示後に消す。

## 6. Web 層 (internal/web)

### ルーティングとガード
- `http.ServeMux` のメソッド付きパターン (Go 1.22+) を使う。登録は3種のガード経由に限定する：
  - `Public`：ログイン不要 (ログイン画面、静的ファイル)
  - `Page`：ログイン必須。未ログインは `/login?next=<エンコード済み>` へ誘導し「ログインしてください。」を flash
  - `AdminPage`：管理者のみ。スタッフは 403
- ハンドラーは `func(w, r) error` の形に統一し、エラー種別で応答を決める (`*HTTPError` はそのコード、DB の混雑は 503、その他は 500 のエラーページ)。
- ミドルウェア順序：リクエスト状態の用意 → パニック回復 → no-store → スキーマ確認 (503 案内) → CSRF → ドットセグメント除去 → ServeMux。スキーマ未整備のときは CSRF より先に 503 案内を返す (Flask 版の before_request と同じ優先順位)。

### スキーマ未整備時の 503
DB が未作成/旧版の間は全画面を 503 の案内ページにし、`inventory init-db` の実行で再起動なしに回復する (Flask 版の `require_current_schema` と同じ運用)。サーバー自体は DB がなくても起動する。

### テンプレート
- `html/template` を使用。継承がないため、`base.html` の骨格に各ページの `title` / `content` ブロックを差し込む構成で Jinja の継承を置き換えた。共通部品は `_` 始まりのファイルに define として置く (`_macros.html` の csrf・数量入力、`_notes.html` の備考部品)。
- テンプレートと静的ファイルは `embed` でバイナリに埋め込む。日時は `jst` 関数で UTC → JST 表示 (Flask 版の同名フィルタと同じ)。
- エスケープ差異：`html/template` は `+` を `&#43;` にする。ブラウザ表示は同一で、ソース上の見た目だけが Flask 版と異なる (既知・許容)。

### 静的ファイル
- 埋め込みファイルの一覧表にある名前だけを配る (URL からファイルパスを組み立てないので、パストラバーサルの余地がない)。
- ETag は内容ハッシュ、`Cache-Control: no-cache` で毎回確認させ、未変更なら 304 (Flask 版の static と同じ振る舞い)。HTML 本体は no-store。
- パスに `.` / `..` セグメントを含むリクエストは `rejectDotSegments` が手前で 404 にする。ServeMux の正規化リダイレクトはステータスが Go の版で変わる (1.22 は 301、現行は 307) ため、版に依存しないよう自前で受けることにした判断。守りの本体は一覧表方式であり、これは見え方を Flask 版 (その場で 404) に揃える層。

## 7. Flask 版との意図的な差分

| 項目 | Flask 版 | Go 版 | 理由 |
| --- | --- | --- | --- |
| 切替直後のセッション | ― | 全員1回ログインし直し | Cookie 形式が異なるため。認証情報は共用 |
| `next` の検証 | 一部の抜けあり (`\` 始まり等) | より厳格 | ブラウザが `\` を `/` と解釈する open redirect の穴を塞ぐ |
| remember Cookie の属性 | SameSite/Secure なし | SameSite=Lax、Secure 設定に連動 | 安全性の底上げ。ログイン動作は不変 |
| CLI の異常系 | トレースバックで落ちる場面がある (DB 未作成で seed 等) | 案内メッセージ＋終了コード1 | 運用時の分かりやすさ |
| メソッド違い (405) | エラーページ | 標準のテキスト応答 | 影響範囲が小さく、標準に寄せた |
| DB 混雑時 | エラーになりうる | BUSY リトライで待つ (3章) | 並行稼働・CLI 同時実行への耐性 |

これ以外 (画面の文言・並び順・計算・CSV のバイト列・リダイレクト先など) は一致を正とし、差異を見つけたらバグとして扱う。

## 8. CLI (internal/cli)

- バイナリ自身がサブコマンドを持つ (Flask 版の `flask <cmd>` に相当)：`serve` (既定) / `init-db` / `seed` / `create-user` / `list-users` / `help`。文言と終了コードは Flask 版 CLI に合わせ、案内文のコマンド名だけ `inventory` に置換。
- 設定解決は Flask 版 `config.py` と同じ規則：DB は `INVENTORY_DATABASE` → `<baseDir>/instance/inventory.db`。秘密鍵の置き場所 (instanceDir) は DB の場所に連動させない (Flask 版も同様)。
- `baseDir` はバイナリの置き場所。ファイルの有無で挙動が変わるコマンド (list-users 等) は、開く前に存在確認をし、空の DB を誤って作らない (Flask 版のコメントにある注意と同じ)。
- パスワードの対話入力は端末のエコーを切る (x/sys の termios を使用し、追加モジュールを増やさない)。パイプ入力時は通常読み。
- serve は SIGINT/SIGTERM で graceful shutdown する。

## 9. テスト戦略

Flask 版のテスト151件を正本として移植し、最終的に192件。期待値の作り方が要点：

- **ベクトル照合** (`tools/gen_util_vectors.py`)：`to_int`・`strip`・`size_key` (647件) など、Python の出力を JSON にして Go の単体テストと突き合わせる。
- **ゴールデン照合** (`tools/gen_golden.py`)：Flask 版のサービスで作った DB (特殊文字・数式注入文字列・在庫0・停止中・同順位カテゴリー等を含む) と、その CSV バイト列・一覧77条件の並び/件数/合計を正解とし、Go が同じ出力を作れることを確かめる。
- **ハッシュ照合** (`tools/gen_hash_vectors.py` / `crosscheck_hash.py`)：双方向 (Flask 生成 → Go 検証、Go 生成 → Werkzeug 検証)。
- **相互運用テスト** (`internal/web/interop_test.go`)：Flask 版が作った DB (Werkzeug ハッシュ入り) で Go 版にログインできること。
- **画面比較**：同じ DB で両実装を起動し、主要15画面の表示テキストが完全一致することを確認した。
- **並行稼働の実地検証**：両サーバーを同時起動し、相互の入出荷・備考・パスワード変更の反映と、並行書き込みの整合を確認した (手順6)。
- **変異確認**：外部キー無効化、BEGIN IMMEDIATE の弱体化、ロールバック省略などの変異を入れ、テストが実際に検出できることを要所で確認している。

Flask 版が変わったら生成スクリプトを再実行し、差分 = 移植漏れとして扱う運用を想定する。

## 10. ビルドと配布

- `make build`：`CGO_ENABLED=0 go build -trimpath -ldflags "-s -w"`。静的リンクの単一バイナリ (実測 13.5 MiB、Go 1.27.3)。Bootstrap 等の静的ファイル約313KB も埋め込み済み。
- mattn 版は 11.1 MiB だが glibc が必要。配布は純 Go 版のみとし、mattn は検証用に留める。
- UPX 等の圧縮は任意 (手順0の試作では約1/3になった実績あり)。
