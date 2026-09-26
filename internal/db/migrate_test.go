package db

import (
	"regexp"
	"testing"
)

// 004는 이미 구독자가 있는 DB에 적용된다(운영 DB). 옛 스키마에 구독자를 넣어두고 004를 적용해서
// 기존 구독자 모두에게 서로 다른 토큰이 채워지는지 확인한다.
func TestMigration004_BackfillsAccessTokens(t *testing.T) {
	conn, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1) // :memory:는 연결마다 별도 DB라 하나로 고정
	t.Cleanup(func() { _ = conn.Close() })

	apply := func(name string) {
		t.Helper()
		b, err := migrationsFS.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if _, err := conn.Exec(string(b)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	for _, n := range []string{"001_init.sql", "002_authored_at.sql", "003_delivery_log_repo_sent.sql"} {
		apply(n)
	}
	if _, err := conn.Exec(`INSERT INTO subscribers(email) VALUES ('a@x.co'), ('b@x.co'), ('c@x.co')`); err != nil {
		t.Fatal(err)
	}

	apply("004_access_token.sql")

	rows, err := conn.Query(`SELECT email, access_token FROM subscribers ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	hex64 := regexp.MustCompile(`^[0-9a-f]{64}$`)
	seen := map[string]string{}
	n := 0
	for rows.Next() {
		var email string
		var tok *string
		if err := rows.Scan(&email, &tok); err != nil {
			t.Fatal(err)
		}
		n++
		if tok == nil || !hex64.MatchString(*tok) {
			t.Fatalf("%s: token = %v, want 64 lowercase hex chars", email, tok)
		}
		if other, dup := seen[*tok]; dup {
			t.Fatalf("%s and %s got the same token", email, other)
		}
		seen[*tok] = email
	}
	if n != 3 {
		t.Fatalf("got %d subscribers, want 3", n)
	}

	// 같은 토큰을 두 구독자에게 줄 수 없다
	if _, err := conn.Exec(`UPDATE subscribers SET access_token = (SELECT access_token FROM subscribers WHERE email='a@x.co') WHERE email='b@x.co'`); err == nil {
		t.Error("duplicate access_token was accepted; the unique index is missing")
	}

	// 아직 토큰이 없는 새 구독자가 여러 명 있어도 된다(가입 직후 발급 전 상태)
	if _, err := conn.Exec(`INSERT INTO subscribers(email) VALUES ('d@x.co'), ('e@x.co')`); err != nil {
		t.Errorf("subscribers without a token should be allowed: %v", err)
	}
}
