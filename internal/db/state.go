package db

import (
	"context"
	"errors"
	"io/fs"
	"os"
)

// State は起動時のDBの状態 (Flask 版の「準備中」503画面の判定に相当)。
type State int

const (
	StateMissing  State = iota // ファイルが無い (初回起動)
	StateOutdated              // スキーマが古い (Migrate が必要)
	StateCurrent               // 最新
	StateForeign               // この版が作ったDBではない (SQLAlchemy 版など)。書き込まない
)

func (s State) String() string {
	return [...]string{"missing", "outdated", "current", "foreign"}[s]
}

// Check はDBファイルの状態を調べる。ファイルが無いときは、作らずに StateMissing を返す
// (存在しないファイルに接続すると、空のDBが勝手に作られてしまうため)。
func Check(ctx context.Context, path string) (State, error) {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return StateMissing, nil
	} else if err != nil {
		return 0, err
	}
	d, err := Open(path)
	if err != nil {
		return 0, err
	}
	defer d.Close()
	v, err := CurrentVersion(ctx, d)
	if err != nil {
		return 0, err
	}
	if v == 0 {
		fe, err := detectForeign(ctx, d)
		if err != nil {
			return 0, err
		}
		if fe != nil {
			return StateForeign, nil
		}
	}
	if IsCurrent(v) {
		return StateCurrent, nil
	}
	return StateOutdated, nil
}

// DescribeForeign は、StateForeign のDBについて、どの種類の他版か (Alembic の記録があるか) を返す。
// 他版のDBでなければ nil。ファイルが無い・開けないときは error。
func DescribeForeign(ctx context.Context, path string) (*ForeignSchemaError, error) {
	d, err := Open(path)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	v, err := CurrentVersion(ctx, d)
	if err != nil || v != 0 {
		return nil, err
	}
	return detectForeign(ctx, d)
}
