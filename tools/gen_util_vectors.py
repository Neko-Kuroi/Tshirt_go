"""Flask 版の utils (to_int / size_key) と Python の str.strip() の出力を、Go 側の照合データにする。

使い方: python tools/gen_util_vectors.py <Flask版の inventory_app のパス>
Go 側は internal/util/testdata/*.json を読んで、同じ入力に同じ結果を返すことを確かめる。
"""
import json
import pathlib
import random
import sys

app_dir = pathlib.Path(sys.argv[1]).resolve()
sys.path.insert(0, str(app_dir))
from app.utils import INT64_MAX, safe_next, size_key, to_int  # noqa: E402

out = pathlib.Path(__file__).resolve().parent.parent / "internal/util/testdata"
out.mkdir(parents=True, exist_ok=True)

# ---- to_int ----
toint_inputs = [
    "0", "7", "12", " 12 ", "\t12\n", "+5", "-5", "+-5", "--5", "- 5", "+ 5",
    "1_000", "1_0_0", "_1", "1_", "1__0", "+_1", "-1_", "00_0",
    "１２", "１_２", "٣", "٣٣", "१२३", "𝟏𝟐", "๑๒", "١٢٣", "٠", "+１２", "1２3",
    "²", "¹²³", "③", "½", "", "  ", "+", "-", "0x10", "0b1", "0o7", "1e3", "1.0", "1,000",
    "007", "-0", "+0",
    str(INT64_MAX), str(INT64_MAX + 1), str(-INT64_MAX), str(-INT64_MAX - 1), "9" * 30, "9" * 5000,
    "1" + "0" * 18, "1" + "0" * 19, "18446744073709551615", "18446744073709551616",
    "\u00a012\u00a0", "\u30005\u3000", "\x1c5\x1d", "\u200b5", "5\u200b", "1 2", "5\x00", "\x005",
    "abc", "１２abc",
]
toint = [{"in": s, "out": to_int(s)} for s in toint_inputs]
(out / "toint.json").write_text(json.dumps(toint, ensure_ascii=False, indent=0), encoding="utf-8")

# ---- str.strip() ----
strip_inputs = [" a ", "\x1ca\x1f", "\u00a0a\u3000", "\u200ba\u200b", "\u180ea", "\u2028a\u2029", "\u0085a",
                "\ufeffa", "a b", "", "   ", "\u2000\u2001a\u200a", "\u202fa\u205f", "\t\n\v\f\r a \r\n",
                "\x1b a", "\u1680a\u1680", "あ　い", "　　"]
strip = [{"in": s, "out": s.strip()} for s in strip_inputs]
(out / "strip.json").write_text(json.dumps(strip, ensure_ascii=False, indent=0), encoding="utf-8")

# ---- size_key による並び ----
fixed = ["XXS", "xs", "Xs", "S", "m", "L", "xl", "2XL", "2xl", "XXL", "3XL", "XXXL", "4XL", "5XL",
         "FREE", "free", "F", "f", "ſ", "ﬀ", "ß", "28", "30", "9", "007", "7", "0", "00", "000", "²", "¹²³",
         "１２", "12", "99999999999999999999", "10000000000000000000", "é", "e\u0301", "M ", "  M", "",
         "XL", "XL", "10", "010", "9", "ＸＬ", "Ｌ"]
rnd = random.Random(20261101)
alphabet = ["X", "S", "M", "L", "F", "R", "E", "2", "3", "0", "1", "9", "x", "s", "m", "l", "é", "e",
            "\u0301", "²", "１", "٣", " ", "ſ", "ß", "ﬀ", "-", "."]
generated = ["".join(rnd.choice(alphabet) for _ in range(rnd.randint(0, 6))) for _ in range(600)]
sizes = fixed + generated
(out / "sizekey.json").write_text(
    json.dumps({"input": sizes, "sorted": sorted(sizes, key=size_key)}, ensure_ascii=False, indent=0),
    encoding="utf-8")
# ---- safe_next ----
# Go 版は、制御文字を含むもの・"/\\" で始まるものを、Python 版より厳しく拒否する (SafeNext のコメント参照)。
# そのため、この一覧は「Python 版と同じ結果になるはずの入力」だけ。厳しくした分は Go 側のテストに別に書く。
next_inputs = ["", "/", "/blank", "/blank/list?category=1&q=a%20b", "/a/b/c", "/login", "//evil.example",
               "///evil.example", "https://evil.example/", "http://evil.example/", "javascript:alert(1)",
               "evil", "blank", "?a=1", "#x", "/?next=//evil.example", "/%2F%2Fevil.example", "/\\evil.example",
               "/ spaced", "/日本語", "/a:b", "/path;params", "\\evil.example", "mailto:a@b.c", " /blank",
               "//", "/.", "/..", "/a//b", "/a\\b"]
safe = [{"in": s, "out": safe_next(s)} for s in next_inputs]
(out / "safe_next.json").write_text(json.dumps(safe, ensure_ascii=False, indent=0), encoding="utf-8")

print(len(toint), "to_int,", len(strip), "strip,", len(sizes), "size_key strings")
