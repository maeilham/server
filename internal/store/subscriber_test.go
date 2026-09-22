package store_test

import (
	"context"
	"errors"
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

func TestSubscriberByAccessToken_Found(t *testing.T) {
	ctx := context.Background()
	subs := store.NewSubscriberStore(newTestDB(t))

	id, err := subs.Upsert(ctx, "a@x.co")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := subs.EnsureAccessToken(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	sess, err := subs.SubscriberByAccessToken(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	if sess.ID != id {
		t.Errorf("ID = %d, want %d", sess.ID, id)
	}
	if sess.Confirmed {
		t.Error("Confirmed = true, want false (just upserted, never confirmed)")
	}
}

func TestSubscriberByAccessToken_UnknownToken(t *testing.T) {
	subs := store.NewSubscriberStore(newTestDB(t))
	unknown := "abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd" // 64 hex chars, not issued to anyone
	_, err := subs.SubscriberByAccessToken(context.Background(), unknown)
	if !errors.Is(err, store.ErrSubscriberNotFound) {
		t.Errorf("err = %v, want ErrSubscriberNotFound", err)
	}
}

func TestSubscriberByAccessToken_Unsubscribed(t *testing.T) {
	ctx := context.Background()
	subs := store.NewSubscriberStore(newTestDB(t))

	id, err := subs.Upsert(ctx, "a@x.co")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := subs.EnsureAccessToken(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := subs.Unsubscribe(ctx, "a@x.co"); err != nil {
		t.Fatal(err)
	}

	// 해지한 사람은 "모르는 토큰"과 같은 에러로 합쳐진다(상태가 새지 않게).
	if _, err := subs.SubscriberByAccessToken(ctx, tok); !errors.Is(err, store.ErrSubscriberNotFound) {
		t.Errorf("err = %v, want ErrSubscriberNotFound", err)
	}
}

func TestConfirmByAccessToken_FirstCallConfirms(t *testing.T) {
	ctx := context.Background()
	subs := store.NewSubscriberStore(newTestDB(t))

	id, err := subs.Upsert(ctx, "a@x.co")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := subs.EnsureAccessToken(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	gotID, newly, err := subs.ConfirmByAccessToken(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	if !newly {
		t.Error("wasNewlyConfirmed = false, want true on first call")
	}
	if gotID != id {
		t.Errorf("id = %d, want %d", gotID, id)
	}

	sess, err := subs.SubscriberByAccessToken(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	if !sess.Confirmed {
		t.Error("subscriber should be confirmed after ConfirmByAccessToken")
	}
}

func TestConfirmByAccessToken_SecondCallIsNoop(t *testing.T) {
	ctx := context.Background()
	subs := store.NewSubscriberStore(newTestDB(t))

	id, err := subs.Upsert(ctx, "a@x.co")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := subs.EnsureAccessToken(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := subs.ConfirmByAccessToken(ctx, tok); err != nil {
		t.Fatal(err)
	}

	gotID, newly, err := subs.ConfirmByAccessToken(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	if newly {
		t.Error("wasNewlyConfirmed = true, want false on second call (already confirmed)")
	}
	if gotID != id {
		t.Errorf("id = %d, want %d", gotID, id)
	}
}

func TestConfirmByAccessToken_UnknownToken(t *testing.T) {
	subs := store.NewSubscriberStore(newTestDB(t))
	_, newly, err := subs.ConfirmByAccessToken(context.Background(), "abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd")
	if !errors.Is(err, store.ErrSubscriberNotFound) {
		t.Errorf("err = %v, want ErrSubscriberNotFound", err)
	}
	if newly {
		t.Error("wasNewlyConfirmed = true for an unknown token")
	}
}

func TestConfirmByAccessToken_Unsubscribed(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	subs := store.NewSubscriberStore(db)

	id, err := subs.Upsert(ctx, "a@x.co")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := subs.EnsureAccessToken(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := subs.Unsubscribe(ctx, "a@x.co"); err != nil {
		t.Fatal(err)
	}

	if _, _, err := subs.ConfirmByAccessToken(ctx, tok); !errors.Is(err, store.ErrSubscriberNotFound) {
		t.Errorf("err = %v, want ErrSubscriberNotFound", err)
	}

	// 시도했다고 confirmed_at이 채워지면 안 된다.
	var isNull bool
	if err := db.QueryRow(`SELECT confirmed_at IS NULL FROM subscribers WHERE id = ?`, id).Scan(&isNull); err != nil || !isNull {
		t.Fatalf("confirmed_at should still be NULL after a rejected attempt (isNull=%v, err=%v)", isNull, err)
	}
}

// 같은 링크를 동시에 두 번 열어도 확인은 한 번만 일어나야 한다(중복 구독 생성 방지의 전제).
func TestConfirmByAccessToken_ConcurrentCallsConfirmOnce(t *testing.T) {
	conn, err := dbpkg.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := dbpkg.Migrate(conn); err != nil {
		t.Fatal(err)
	}
	subs := store.NewSubscriberStore(conn)
	ctx := context.Background()
	id, err := subs.Upsert(ctx, "race@x.co")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := subs.EnsureAccessToken(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	const n = 20
	newly := make([]bool, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, newly[i], errs[i] = subs.ConfirmByAccessToken(ctx, tok)
		}()
	}
	wg.Wait()

	count := 0
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("call %d: %v", i, errs[i])
		}
		if newly[i] {
			count++
		}
	}
	if count != 1 {
		t.Errorf("wasNewlyConfirmed=true count = %d, want exactly 1", count)
	}
}
