"""Go が作ったパスワードハッシュを、Werkzeug の check_password_hash で確かめる (Flask 版へ戻せることの確認)。

  GO_HASH_OUT=/tmp/go_hashes.json go test ./internal/auth -run Crosscheck
  python tools/crosscheck_hash.py /tmp/go_hashes.json
"""
import json
import sys

from werkzeug.security import check_password_hash

cases = json.load(open(sys.argv[1], encoding="utf-8"))
bad = [c for c in cases if not check_password_hash(c["hash"], c["pw"]) or check_password_hash(c["hash"], c["pw"] + "x")]
print(f"{len(cases)} Go-made hashes checked by Werkzeug; failures: {len(bad)}")
sys.exit(1 if bad else 0)
