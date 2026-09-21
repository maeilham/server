package store_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	dbpkg "github.com/maeilham/server/internal/db"
	"github.com/maeilham/server/internal/pkg/token"
	"github.com/maeilham/server/internal/store"
)

func TestEnsureAccessToken_CreatesOnceAndIsStable(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	subs := store.NewSubscriberStore(db)

	a, err := subs.Upsert(ctx, "a@x.co")
	if err != nil {
		t.Fatal(err)
	}
	// 가입 직후에는 토큰이 없다. 발급은 EnsureAccessToken이 한다
	var isNull bool
	if err := db.QueryRow(`SELECT access_token IS NULL FROM subscribers WHERE id = ?`, a).Scan(&isNull); err != nil || !isNull {
		t.Fatalf("access_token should be NULL right after Upsert (isNull=%v, err=%v)", isNull, err)
	}

	first, err := subs.EnsureAccessToken(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if !token.IsAccessToken(first) {
		t.Fatalf("token %q is not in access-token format", first)
	}
	// 링크를 다시 보내도 같은 토큰이어야 즐겨찾기가 안 깨진다
	again, err := subs.EnsureAccessToken(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if again != first {
		t.Errorf("second call returned %q, want the same %q", again, first)
	}

	b, _ := subs.Upsert(ctx, "b@x.co")
	other, err := subs.EnsureAccessToken(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	if other == first {
		t.Error("two subscribers share a token")
	}
}

func TestEnsureAccessToken_UnknownSubscriber(t *testing.T) {
	subs := store.NewSubscriberStore(newTestDB(t))
	if tok, err := subs.EnsureAccessToken(context.Background(), 9999); err == nil {
		t.Errorf("want an error for an unknown subscriber, got token %q", tok)
	}
}

// 같은 가입 요청이 동시에 여러 번 와도(더블 클릭 등) 모두 같은 토큰을 받아야 한다.
// :memory:는 연결마다 별도 DB라서 이 테스트는 파일 DB를 쓴다.
func TestEnsureAccessToken_Concurrent(t *testing.T) {
	conn, err := dbpkg.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := dbpkg.Migrate(conn); err != nil {
		t.Fatal(err)
	}
	subs := store.NewSubscriberStore(conn)
	id, err := subs.Upsert(context.Background(), "race@x.co")
	if err != nil {
		t.Fatal(err)
	}

	const n = 20
	got := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i], errs[i] = subs.EnsureAccessToken(context.Background(), id)
		}()
	}
	wg.Wait()

	for i := range n {
		if errs[i] != nil {
			t.Fatalf("call %d: %v", i, errs[i])
		}
		if got[i] != got[0] {
			t.Fatalf("call %d returned %q, call 0 returned %q", i, got[i], got[0])
		}
	}
}
