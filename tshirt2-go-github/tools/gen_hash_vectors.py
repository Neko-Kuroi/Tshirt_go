"""Werkzeug が実際に作るパスワードハッシュ (scrypt / pbkdf2) を、Go 側の検証用データにする。

使い方: python tools/gen_hash_vectors.py   (Werkzeug が必要)
"""
import json
import pathlib

from werkzeug.security import check_password_hash, generate_password_hash

cases = []
for i, pw in enumerate(["password123", "テストpass123", "a" * 40, "p@ss word\t!", "あ" * 8]):
    # pbkdf2 は反復回数が多くて遅いので、最初の2つのパスワードだけ (テストの実行時間を抑えるため)
    for method in ["scrypt"] + (["pbkdf2:sha256", "pbkdf2:sha1", "pbkdf2:sha512"] if i < 2 else []):
        h = generate_password_hash(pw, method=method)
        assert check_password_hash(h, pw)
        cases.append({"pw": pw, "hash": h})
out = pathlib.Path(__file__).resolve().parent.parent / "internal/auth/testdata/werkzeug_hashes.json"
out.write_text(json.dumps(cases, ensure_ascii=False, indent=1), encoding="utf-8")
print(len(cases), "hashes ->", out)
