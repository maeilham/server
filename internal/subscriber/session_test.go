package subscriber

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	dbpkg "github.com/maeilham/server/internal/db"
	"github.com/maeilham/server/internal/store"
)

// newTestServiceWithActiveRepo는 newTestService와 같지만, AddAllActiveRepoSubscriptions가
// 실제로 구독을 만들 수 있게 활성 repo를 하나 미리 심어둔다(마이그레이션은 repo를 심지 않는다).
func newTestServiceWithActiveRepo(t *testing.T) (*SubscriberService, *fakeMailer, store.SubscriberRepository, *sql.DB) {
	t.Helper()
	conn, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = conn.Close() })
	if err := dbpkg.Migrate(conn); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(
		`INSERT INTO repos (slug, github_url, display_name, active) VALUES ('bops', 'https://x', 'bops', 1)`,
	); err != nil {
		t.Fatal(err)
	}
	mailer := &fakeMailer{}
	repo := store.NewSubscriberStore(conn)
	svc := NewSubscriberService(repo, mailer, "test-secret", "https://api.example", "https://web.example/")
	return svc, mailer, repo, conn
}

func TestEstablishSession_FirstTimeConfirmsAndSubscribes(t *testing.T) {
	svc, mailer, repo, conn := newTestServiceWithActiveRepo(t)
	ctx := context.Background()

	if err := svc.Subscribe(ctx, "me@example.com", nil); err != nil {
		t.Fatal(err)
	}
	m := linkRe.FindStringSubmatch(mailer.sent[0].TextBody)
	if m == nil {
		t.Fatalf("no personal link in mail:\n%s", mailer.sent[0].TextBody)
	}
	tok := m[1]

	newly, err := svc.EstablishSession(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	if !newly {
		t.Error("wasNewlyConfirmed = false, want true on first open")
	}
	var confirmed bool
	if err := conn.QueryRow(`SELECT confirmed_at IS NOT NULL FROM subscribers WHERE email = ?`, "me@example.com").Scan(&confirmed); err != nil {
		t.Fatal(err)
	}
	if !confirmed {
		t.Error("subscriber should be confirmed after EstablishSession")
	}

	id, err := repo.Upsert(ctx, "me@example.com") // idempotent, just to get the id
	if err != nil {
		t.Fatal(err)
	}
	subs, err := repo.LoadSubscriptions(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 || subs[0].RepoSlug != "bops" {
		t.Errorf("subscriptions = %+v, want one subscription to bops", subs)
	}
}

func TestEstablishSession_SecondCallIsNoop(t *testing.T) {
	svc, mailer, repo, conn := newTestServiceWithActiveRepo(t)
	ctx := context.Background()

	if err := svc.Subscribe(ctx, "me@example.com", nil); err != nil {
		t.Fatal(err)
	}
	tok := linkRe.FindStringSubmatch(mailer.sent[0].TextBody)[1]

	if _, err := svc.EstablishSession(ctx, tok); err != nil {
		t.Fatal(err)
	}
	id, err := repo.Upsert(ctx, "me@example.com")
	if err != nil {
		t.Fatal(err)
	}
	// 두 번째 호출 전에 구독을 하나 수동으로 더 넣어둔다. 이 repo는 일부러 비활성으로 만들어서,
	// EstablishSession이 두 번째에도 ClearSubscriptions+AddAllActiveRepoSubscriptions를 다시
	// 돌린다면(옛 Confirm처럼) 이 행이 사라지고 다시 채워지지 않는다는 걸로 감지한다.
	if _, err := conn.Exec(
		`INSERT INTO repos (slug, github_url, display_name, active) VALUES ('manually-added', 'https://x', 'x', 0)`,
	); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddSubscription(ctx, id, "manually-added", 3); err != nil {
		t.Fatal(err)
	}

	newly, err := svc.EstablishSession(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	if newly {
		t.Error("wasNewlyConfirmed = true, want false on second open (already confirmed)")
	}

	var stillThere bool
	if err := conn.QueryRow(
		`SELECT EXISTS(SELECT 1 FROM subscriptions WHERE subscriber_id = ? AND repo_slug = 'manually-added')`, id,
	).Scan(&stillThere); err != nil {
		t.Fatal(err)
	}
	if !stillThere {
		t.Error("manually added subscription was cleared; EstablishSession is not idempotent for subscriptions")
	}
}

func TestEstablishSession_UnknownToken(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	unknown := "abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd"
	if _, err := svc.EstablishSession(context.Background(), unknown); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("err = %v, want ErrUnauthorized", err)
	}
}

func TestEstablishSession_MalformedToken(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	for _, tok := range []string{"", "too-short", "ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ"} {
		if _, err := svc.EstablishSession(context.Background(), tok); !errors.Is(err, ErrUnauthorized) {
			t.Errorf("token %q: err = %v, want ErrUnauthorized", tok, err)
		}
	}
}

func TestEstablishSession_Unsubscribed(t *testing.T) {
	svc, mailer, repo, _ := newTestService(t)
	ctx := context.Background()

	if err := svc.Subscribe(ctx, "me@example.com", nil); err != nil {
		t.Fatal(err)
	}
	tok := linkRe.FindStringSubmatch(mailer.sent[0].TextBody)[1]
	if _, err := svc.EstablishSession(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if err := repo.Unsubscribe(ctx, "me@example.com"); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.EstablishSession(ctx, tok); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("err = %v, want ErrUnauthorized", err)
	}
}

func TestSessionStatus_ValidConfirmedToken(t *testing.T) {
	svc, mailer, _, _ := newTestService(t)
	ctx := context.Background()

	if err := svc.Subscribe(ctx, "me@example.com", nil); err != nil {
		t.Fatal(err)
	}
	tok := linkRe.FindStringSubmatch(mailer.sent[0].TextBody)[1]
	if _, err := svc.EstablishSession(ctx, tok); err != nil {
		t.Fatal(err)
	}

	if err := svc.SessionStatus(ctx, tok); err != nil {
		t.Errorf("err = %v, want nil", err)
	}
}

func TestSessionStatus_UnconfirmedTokenIsUnauthorized(t *testing.T) {
	svc, mailer, _, row := newTestService(t)
	ctx := context.Background()

	if err := svc.Subscribe(ctx, "me@example.com", nil); err != nil {
		t.Fatal(err)
	}
	tok := linkRe.FindStringSubmatch(mailer.sent[0].TextBody)[1]

	if err := svc.SessionStatus(ctx, tok); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("err = %v, want ErrUnauthorized", err)
	}
	if _, confirmed := row("me@example.com"); confirmed {
		t.Error("SessionStatus must not confirm the subscriber as a side effect")
	}
}

func TestSessionStatus_UnknownToken(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	unknown := "abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd"
	if err := svc.SessionStatus(context.Background(), unknown); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("err = %v, want ErrUnauthorized", err)
	}
}

func TestSessionStatus_Unsubscribed(t *testing.T) {
	svc, mailer, repo, _ := newTestService(t)
	ctx := context.Background()

	if err := svc.Subscribe(ctx, "me@example.com", nil); err != nil {
		t.Fatal(err)
	}
	tok := linkRe.FindStringSubmatch(mailer.sent[0].TextBody)[1]
	if _, err := svc.EstablishSession(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if err := repo.Unsubscribe(ctx, "me@example.com"); err != nil {
		t.Fatal(err)
	}

	if err := svc.SessionStatus(ctx, tok); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("err = %v, want ErrUnauthorized", err)
	}
}
