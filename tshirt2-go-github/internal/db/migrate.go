package db

import (
	"context"
	"database/sql"
	"fmt"
)

// ForeignSchemaError は、この版が作ったものではないスキーマのDBを開いたことを表す。
//
// Flask 版には、SQLAlchemy + Alembic 版で作られたDBを引き継ぐ処理があるが、Go 版は
// 引き継ぎを行わない (2026-11-01 の決定)。user_version が 0 なのに表がある DB に
// v1 の CREATE TABLE を流すと中途半端に壊れるので、書き込まずに止める。
type ForeignSchemaError struct {
	Alembic bool // alembic_version 表がある (= SQLAlchemy + Alembic 版で作られたDB)
}

func (e *ForeignSchemaError) Error() string {
	if e.Alembic {
		return "SQLAlchemy 版で作成されたDBです。この版は対応していません。" +
			"Flask 版で user_version 方式へ移行してから使ってください"
	}
	return "user_version が未設定の既存DBです (この版が作ったDBではありません)。" +
		"Flask 版で user_version 方式へ移行してから使ってください"
}

// CurrentVersion は PRAGMA user_version (適用済みのスキーマ番号) を返す。
func CurrentVersion(ctx context.Context, q Querier) (int, error) {
	var v int
	err := q.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v)
	return v, err
}

// IsCurrent は、そのバージョンが最新のスキーマか (Flask 版 is_current と同じく >=)。
func IsCurrent(version int) bool { return version >= len(Migrations) }

func tableExists(ctx context.Context, q Querier, name string) (bool, error) {
	var one int
	err := q.QueryRowContext(ctx,
		"SELECT 1 FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

// detectForeign は user_version=0 のDBが、空 (新規) か、他の版が作った既存DBかを調べる。
func detectForeign(ctx context.Context, q Querier) (*ForeignSchemaError, error) {
	hasCategory, err := tableExists(ctx, q, "category")
	if err != nil {
		return nil, err
	}
	hasAlembic, err := tableExists(ctx, q, "alembic_version")
	if err != nil {
		return nil, err
	}
	if hasCategory || hasAlembic {
		return &ForeignSchemaError{Alembic: hasAlembic}, nil
	}
	return nil, nil
}

// Migrate は未適用のマイグレーションを順に適用し、最終的なバージョンを返す。
// 各版は1つのトランザクションで適用し、失敗したらその版の変更を全部取り消す。
func Migrate(ctx context.Context, d *DB) (int, error) {
	return migrateWith(ctx, d, Migrations)
}

func migrateWith(ctx context.Context, d *DB, migs []string) (int, error) {
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
			return 0, fe
		}
	}
	for n := v; n < len(migs); n++ {
		err := d.Tx(ctx, func(c *sql.Conn) error {
			// 別プロセスが先に適用していないか、書き込みロックを取ってから確かめ直す
			cur, err := CurrentVersion(ctx, c)
			if err != nil {
				return err
			}
			if cur > n {
				return nil
			}
			if _, err := c.ExecContext(ctx, migs[n]); err != nil {
				return fmt.Errorf("マイグレーション %d: %w", n+1, err)
			}
			_, err = c.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version=%d", n+1))
			return err
		})
		if err != nil {
			return 0, err
		}
	}
	return CurrentVersion(ctx, d)
}
