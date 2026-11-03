package db

// Tx の SQLITE_BUSY リトライ (指数バックオフ) のテスト。
// retryOnBusy は述語を差し替えられるので単体で確かめ、統合テストでは
// 実際にロックを busy_timeout より長く保持して、再試行で書き込めることを見る。

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

var errBusyStub = errors.New("stub: database is locked")

func stubIsBusy(err error) bool { return errors.Is(err, errBusyStub) }

func TestRetryOnBusySucceedsAfterRetries(t *testing.T) {
	calls := 0
	err := retryOnBusy(ctx, stubIsBusy, func() error {
		calls++
		if calls <= 3 {
			return errBusyStub
		}
		return nil
	})
	if err != nil || calls != 4 {
		t.Errorf("err=%v calls=%d, want nil after 4 calls", err, calls)
	}
}

func TestRetryOnBusyGivesUp(t *testing.T) {
	calls := 0
	err := retryOnBusy(ctx, stubIsBusy, func() error {
		calls++
		return errBusyStub
	})
	if !errors.Is(err, errBusyStub) || calls != busyRetryLimit+1 {
		t.Errorf("err=%v calls=%d, want busy after %d calls", err, calls, busyRetryLimit+1)
	}
}

func TestRetryOnBusyDoesNotRetryOtherErrors(t *testing.T) {
	other := errors.New("constraint failed")
	calls := 0
	err := retryOnBusy(ctx, stubIsBusy, func() error {
		calls++
		return other
	})
	if !errors.Is(err, other) || calls != 1 {
		t.Errorf("err=%v calls=%d, want the original error after 1 call", err, calls)
	}
}

func TestRetryOnBusyStopsOnContextCancel(t *testing.T) {
	cctx, cancel := context.WithCancel(context.Background())
	calls := 0
	err := retryOnBusy(cctx, stubIsBusy, func() error {
		calls++
		cancel() // 1回目の失敗の直後に取り消す → バックオフ中に ctx.Err() で戻る
		return errBusyStub
	})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Errorf("err=%v calls=%d, want context.Canceled after 1 call", err, calls)
	}
}

// TestTxWaitsOutExternalLock は、別ハンドル (別プロセス相当) が書き込みロックを
// busy_timeout (5秒) より長く保持しても、Tx がリトライで最終的に成功することを見る。
// 5秒超の保持が要るため、このテスト自体も6秒ほどかかる。
func TestTxWaitsOutExternalLock(t *testing.T) {
	d1, path := newDB(t)
	mustExec(t, d1, "CREATE TABLE counter (n INTEGER)")
	mustExec(t, d1, "INSERT INTO counter VALUES (0)")
	d2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()

	held := make(chan struct{})
	release := make(chan struct{})
	done1 := make(chan error, 1)
	go func() {
		done1 <- d1.Tx(ctx, func(c *sql.Conn) error {
			if _, err := c.ExecContext(ctx, "UPDATE counter SET n = n + 1"); err != nil {
				return err
			}
			close(held) // BEGIN IMMEDIATE の書き込みロックを保持したまま待つ
			<-release
			return nil
		})
	}()
	<-held

	start := time.Now()
	done2 := make(chan error, 1)
	go func() {
		done2 <- d2.Tx(ctx, func(c *sql.Conn) error {
			_, err := c.ExecContext(ctx, "UPDATE counter SET n = n + 1")
			return err
		})
	}()
	// busy_timeout の5秒を超えて保持してから解放する (1回目の試行は BUSY で落ち、
	// リトライで成功するはず)
	time.Sleep(5500 * time.Millisecond)
	close(release)
	if err := <-done1; err != nil {
		t.Fatalf("holder Tx: %v", err)
	}
	if err := <-done2; err != nil {
		t.Fatalf("waiter Tx: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 5*time.Second {
		t.Errorf("waiter finished in %v, want >= 5s (it should have waited out the lock)", elapsed)
	}
	if n := scalarInt(t, d1, "SELECT n FROM counter"); n != 2 {
		t.Errorf("counter = %d, want 2", n)
	}
}
