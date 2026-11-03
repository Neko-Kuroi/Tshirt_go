"""Flask 版のコードで、紛らわしいデータを持つ検証用 DB を作り、その出力を Go 側の正解データにする。

使い方: python tools/gen_golden.py <Flask版の inventory_app のパス>

生成物 (internal/service/testdata/golden/):
  inventory.db        検証用 DB (Flask 版の services で作ったもの)
  blank*.csv, printed.csv   export.to_csv_bytes の出力そのもの (バイト一致を確かめる)
  stocklist.json      各クエリ文字列での parse 結果・並び順 (variant id の列)・件数・合計
  options.json        絞り込みの選択肢と、タイプの入力候補

Go 側は同じ DB を開いて同じ出力を作り、Python のバイト列・並び順と一致することを確かめる。
"""
import json
import pathlib
import shutil
import sqlite3
import sys
import tempfile

from werkzeug.datastructures import MultiDict
from werkzeug.security import generate_password_hash

app_dir = pathlib.Path(sys.argv[1]).resolve()
sys.path.insert(0, str(app_dir))
sys.path.insert(0, str(app_dir / "tests"))

from app import create_app, repo  # noqa: E402
from app.db import get_db, transaction  # noqa: E402
from app.schema import migrate  # noqa: E402
from app.services import export, stock, stocklist  # noqa: E402
from config import TestConfig  # noqa: E402

out = pathlib.Path(__file__).resolve().parent.parent / "internal/service/testdata/golden"
shutil.rmtree(out, ignore_errors=True)
out.mkdir(parents=True)

tmp = pathlib.Path(tempfile.mkdtemp())


class Cfg(TestConfig):
    DATABASE = str(tmp / "golden.db")


app = create_app(Cfg)
BIG = 10 ** 30

with app.app_context():
    migrate(get_db())
    with transaction() as db:
        db.execute(
            "INSERT INTO category (name, sort_order, uses_color, uses_size, uses_variant_name, is_printable) VALUES "
            "('Tシャツ', 0, 1, 1, 0, 1), ('バッジ', 1, 0, 0, 1, 0), "
            "('キャップ', 2, 1, 0, 0, 1), ('ステッカー', 2, 0, 0, 1, 0)")  # 3 と 4 は sort_order が同じ
        # パスワードは password123 (Werkzeug が作る本物のハッシュ。Go 側のログインの結合テストに使う)
        db.execute('INSERT INTO "user" (username, password_hash, role) VALUES (?, ?, \'admin\'), (?, ?, \'staff\')',
                   ("admin", generate_password_hash("password123"), "staff", generate_password_hash("password123")))
        items = [
            # (category_id, brand, item_no, name, style)
            (1, "Printstar", "085-CVT", "Tシャツ", "半袖"),                  # 1
            (1, "United Athle", "5001", "Tシャツ", "長袖"),                  # 2
            (2, "", "44mm", "缶バッジ", ""),                                  # 3
            (1, "Gildan", "G200", 'A,"B"\nC', "ドライ"),                      # 4 CSV の特殊文字
            (1, "E1", "n", "=1+1", ""), (1, "E2", "n", "+1", ""),            # 5-9 数式注入
            (1, "E3", "n", "-1", ""), (1, "E4", "n", "@SUM(A1)", ""), (1, "E5", "n", "\t=1", ""),
            (1, "Lead", "n", " 先頭に空白", ""),                              # 10 Go の csv と引用符の付け方が分かれる所
            (1, "CR", "n", "x\ry", ""),                                       # 11
            (1, "100%_cotton", "A\\B", "ワイルド", "半袖"),                   # 12 LIKE のワイルドカードと \
            (1, "Old", "1", "停止予定", "長袖"),                              # 13 後で停止中にする
            (3, "CapCo", "C1", "キャップ", ""),                               # 14
            (4, "", "S1", "ステッカー", ""),                                  # 15
            (1, "", "", "ノーブランド", "半袖"),                              # 16 ブランドも品番も空
        ]
        for c, b, n, nm, st in items:
            db.execute("INSERT INTO item (category_id, brand, item_no, name, style) VALUES (?, ?, ?, ?, ?)",
                       (c, b, n, nm, st))

    admin = repo.get_user_by_name("admin")

    def receive(item_id, color, size, qty, variant_name=""):
        with transaction():
            v = stock.get_or_create_variant(repo.get_item(item_id), color, size, variant_name)
            stock.receive(admin, v, qty)
            return v

    sizes = ["XL", "S", "L", "M", "2XL", "FREE", "F", "XXL", "xl", "28", "30", "9", "007", "7", "12",
             "99999999999999999999", "²", "１２", "3L", "é", "e\u0301", "ＸＬ"]
    for i, size in enumerate(sizes):
        receive(1, "黒", size, (i % 7) + 1)
    for color in ("黒", "白", "赤", "あお", "Blue"):
        receive(1, color, "M", 3)
    receive(2, "黒", "M", 5)
    receive(2, "赤", "L", 1)
    receive(3, "", "", 9, variant_name="赤")
    receive(3, "", "", 4, variant_name="デザインA")
    receive(4, "黒", "M", 2)
    for item_id in range(5, 12):
        receive(item_id, "黒", "M", 1)
    receive(12, "黒", "M", 6)
    receive(13, "赤", "L", 2)
    receive(14, "赤", "", 8)
    receive(14, "青", "", 0 + 1)
    receive(15, "", "", 3, variant_name="星")
    receive(16, "白", "S", 1)

    with transaction():  # 在庫0の行 (出荷で0にする)
        v = repo.find_variant(1, "白", "M", "")
        stock.ship(admin, v, 3)
    # プリント変換 (完成数の違い、完成0、キャップ)
    receive(1, "黒", "M", 20)    # 同じ variant から複数回変換するので、在庫を足しておく
    receive(2, "黒", "M", 5)
    v1 = repo.find_variant(1, "黒", "M", "")
    with transaction():
        stock.convert_to_printed(admin, v1, "ねこロゴ", 6, 5)
    with transaction():
        stock.convert_to_printed(admin, v1, 'ロゴ,"B"', 2, 2)
    v2 = repo.find_variant(2, "黒", "M", "")
    with transaction():
        stock.convert_to_printed(admin, v2, "ねこロゴ", 5, 4)
    with transaction():
        stock.convert_to_printed(admin, v2, "全滅", 1, 0)           # 完成0 → printed_product は作られない
    vcap = repo.find_variant(14, "赤", "", "")
    with transaction():
        stock.convert_to_printed(admin, vcap, "キャップロゴ", 3, 3)
    p = [x for x in repo.products() if x["design_name"] == "キャップロゴ"][0]
    with transaction():
        stock.ship(admin, p, 3)                                      # プリント済みで在庫0の行
    with transaction() as db:
        db.execute("UPDATE item SET is_active = 0 WHERE id = 13")    # 停止中 (在庫は残る)

    # ---- 出力 ----
    (out / "blank.csv").write_bytes(export.to_csv_bytes(export.BLANK_HEADERS, export.blank_rows(None)))
    for cid in (1, 2, 3, 999):
        (out / f"blank_cat{cid}.csv").write_bytes(
            export.to_csv_bytes(export.BLANK_HEADERS, export.blank_rows(cid)))
    (out / "printed.csv").write_bytes(export.to_csv_bytes(export.PRINTED_HEADERS, export.printed_rows()))

    queries = [[]]
    for key in stocklist.SORTS:
        queries += [[("sort", key)], [("sort", key), ("dir", "desc")], [("sort", key), ("dir", "asc")]]
    queries += [
        [("style", "半袖")], [("style", "長袖")], [("style", "ドライ")], [("style", "存在しない")], [("style", " 半袖 ")],
        [("category", "1")], [("category", "2")], [("category", "3")], [("category", "4")], [("category", "999")],
        [("category", "-1")], [("category", "abc")], [("category", str(BIG))], [("category", "１")],
        [("category", " 2 ")], [("category", "1_0")], [("category", "")],
        [("color", "黒")], [("color", "赤")], [("color", "白")], [("color", "デザインA")], [("color", "星")],
        [("size", "M")], [("size", "XL")], [("size", "²")], [("size", "１２")], [("size", "007")], [("size", "xl")],
        [("in_stock", "1")], [("in_stock", "yes")], [("in_stock", "1"), ("sort", "qty"), ("dir", "desc")],
        [("q", "United")], [("q", "085")], [("q", "%")], [("q", "_")], [("q", "100%_")], [("q", "\\")],
        [("q", "A\\B")], [("q", "ノーブランド")], [("q", "UNITED")], [("q", " Printstar ")],
        [("style", "半袖"), ("color", "黒"), ("size", "M")],
        [("category", "1"), ("style", "半袖"), ("color", "黒"), ("sort", "size"), ("dir", "desc")],
        [("sort", "evil;DROP TABLE item")], [("sort", "")], [("dir", "sideways")],
        [("sort", "size"), ("sort", "qty")], [("dir", "desc"), ("dir", "asc")],
        [("style", "あ" * 5000)], [("q", "x" * 5000)], [("color", "\x00")], [("_external", "1"), ("endpoint", "x")],
        [("sort", "item"), ("q", "E")], [("sort", "color"), ("dir", "desc"), ("category", "2")],
        [("sort", "style"), ("dir", "desc"), ("in_stock", "1")],
    ]
    cases = []
    for q in queries:
        f = stocklist.parse(MultiDict(q))
        res = stocklist.search(f)
        cases.append({"query": q, "max_rows": None, "filter": f,
                      "ids": [r["id"] for r in res["rows"]], "count": res["count"],
                      "total_qty": res["total_qty"], "truncated": res["truncated"]})
    for q in ([("sort", "size")], [], [("sort", "qty"), ("dir", "desc")]):   # 表示行数の上限
        saved, stocklist.MAX_ROWS = stocklist.MAX_ROWS, 5
        f = stocklist.parse(MultiDict(q))
        res = stocklist.search(f)
        stocklist.MAX_ROWS = saved
        cases.append({"query": q, "max_rows": 5, "filter": f,
                      "ids": [r["id"] for r in res["rows"]], "count": res["count"],
                      "total_qty": res["total_qty"], "truncated": res["truncated"]})
    (out / "stocklist.json").write_text(json.dumps(cases, ensure_ascii=False, indent=0), encoding="utf-8")

    opts = stocklist.options()
    opts["style_choices"] = stocklist.style_choices()
    (out / "options.json").write_text(json.dumps(opts, ensure_ascii=False, indent=0), encoding="utf-8")

    n_variants = get_db().execute("SELECT COUNT(*) AS n FROM variant").fetchone()["n"]
    n_products = get_db().execute("SELECT COUNT(*) AS n FROM printed_product").fetchone()["n"]

# DB を単一ファイルにして保存 (WAL を取り込み、journal_mode=DELETE に戻す)
raw = sqlite3.connect(Cfg.DATABASE)
raw.execute("PRAGMA wal_checkpoint(TRUNCATE)")
raw.execute("PRAGMA journal_mode=DELETE")
raw.close()
shutil.copy(Cfg.DATABASE, out / "inventory.db")
print(f"variants={n_variants} printed_products={n_products} cases={len(cases)} "
      f"blank.csv={(out / 'blank.csv').stat().st_size}B printed.csv={(out / 'printed.csv').stat().st_size}B")
