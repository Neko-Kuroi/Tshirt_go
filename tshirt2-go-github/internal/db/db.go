// Package db は SQLite への接続、書き込みトランザクション、マイグレーションを扱う。
//
// Flask 版 app/db.py と app/schema.py に対応する。スキーマと PRAGMA user_version 方式は
// Flask 版と同一で、同じ inventory.db をそのまま読み書きできる。
package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"math/rand/v2"
	"strings"
	"time"
)

// Querier は *sql.DB / *sql.Conn / *sql.Tx のどれからでも読み取りができるようにする最小の窓口。
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// DB は接続プールの薄いラッパー。
type DB struct {
	*sql.DB
}

var pathEscaper = strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23")

func escapePath(p string) string { return pathEscaper.Replace(p) }

// Open はファイルを開く (無ければ作る)。「無ければ作らない」確認は Check を使う。
func Open(path string) (*DB, error) {
	sdb, err := sql.Open(driverName, dsn(path))
	if err != nil {
		return nil, err
	}
	// 書き込みの直列化は SQLite 側 (BEGIN IMMEDIATE + busy_timeout) が行う。
	// 接続数は少数に絞る (この規模では読み取りの同時実行は要らない)。
	sdb.SetMaxOpenConns(4)
	if err := sdb.Ping(); err != nil {
		sdb.Close()
		return nil, err
	}
	return &DB{sdb}, nil
}

// Tx は書き込み用のトランザクション (Flask 版 transaction() の BEGIN IMMEDIATE ... COMMIT)。
// fn が error を返すか panic すると、全部ロールバックする。
//
// SQLITE_BUSY (他の書き込みが busy_timeout の5秒を超えて終わらない) のときは、
// 指数バックオフでトランザクション全体を再試行する。失敗した試行はロールバック
// 済みなので、fn の再実行は安全。別プロセスが同じ DB を書いている間 (手順6の
// 並行稼働や、サーバー稼働中の CLI 実行) でも、諦めずに待てるようにするため。
//
// 注意: fn の中では、引数の conn だけを使うこと。d.QueryContext など、プールから別の接続を
// 取る呼び出しは、書き込みロックを持つ自分自身を待つことになりうる。
func (d *DB) Tx(ctx context.Context, fn func(conn *sql.Conn) error) error {
	return retryOnBusy(ctx, isBusyError, func() error { return d.txOnce(ctx, fn) })
}

const (
	busyRetryLimit = 5 // 再試行の回数 (初回と合わせて最大6回試す)
	busyRetryBase  = 50 * time.Millisecond
	busyRetryMax   = time.Second
)

// retryOnBusy は、op が busy エラーを返したら指数バックオフで再試行する。
// 1回あたりのロック待ちは busy_timeout (5秒) が既に吸収するので、ここは
// 「5秒待っても駄目だった」ときの保険。busy 以外のエラーはそのまま返す。
func retryOnBusy(ctx context.Context, isBusy func(error) bool, op func() error) error {
	delay := busyRetryBase
	for retry := 0; ; retry++ {
		err := op()
		if err == nil || !isBusy(err) || retry >= busyRetryLimit {
			return err
		}
		// ジッターを付けて、待ち行列の全員が同じ瞬間に再挑戦しないようにする
		wait := delay + time.Duration(rand.Int64N(int64(delay/2)+1))
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
		delay = min(delay*2, busyRetryMax)
	}
}

func (d *DB) txOnce(ctx context.Context, fn func(conn *sql.Conn) error) (err error) {
	conn, err := d.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		// ctx が取り消されていてもロールバックは実行する
		if _, rerr := conn.ExecContext(context.Background(), "ROLLBACK"); rerr != nil {
			// トランザクションが開いたままの接続をプールに戻さない
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()

	if err = fn(conn); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}
