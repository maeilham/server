package content

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// ── 테스트 도구 ──────────────────────────────────────────────────────────────

const validFile = "---\ntitle: \"제목\"\npreview: \"미리보기\"\ntags: [go]\n---\n\n본문입니다.\n"

// fakeFetcher는 GitHub 대역이다. 호출 횟수를 세고, 오류나 지연을 주입할 수 있다.
type fakeFetcher struct {
	mu      sync.Mutex
	calls   int
	body    string
	err     error
	release chan struct{} // nil이 아니면 이 채널이 닫힐 때까지 FetchRaw가 멈춘다
	started chan struct{} // nil이 아니면 첫 호출이 시작될 때 닫힌다
	once    sync.Once
}

func (f *fakeFetcher) FetchRaw(ctx context.Context, owner, repo, ref, path string) ([]byte, error) {
	f.mu.Lock()
	f.calls++
	body, err, release, started := f.body, f.err, f.release, f.started
	f.mu.Unlock()

	if started != nil {
		f.once.Do(func() { close(started) })
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	return []byte(body), nil
}

func (f *fakeFetcher) set(body string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.body, f.err = body, err
}

func (f *fakeFetcher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeClock은 시간을 손으로 흘려보낸다.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newTestSource(f RawFetcher, opts BodyOptions) (*CachedBodySource, *fakeClock) {
	clock := &fakeClock{t: time.Date(2026, 9, 20, 7, 0, 0, 0, time.UTC)}
	s := NewCachedBodySource(f, opts)
	s.now = clock.Now
	return s, clock
}

func ref(path, sha string) BodyRef {
	return BodyRef{Owner: "maeilham", Repo: "backend-ops", Path: path, SHA: sha}
}

// ── 테스트 ───────────────────────────────────────────────────────────────────

func TestBodySource_StripsFrontmatter(t *testing.T) {
	f := &fakeFetcher{body: validFile}
	s, _ := newTestSource(f, BodyOptions{})

	got, err := s.Get(context.Background(), ref("content/0001-a.md", "sha1"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "본문입니다." {
		t.Errorf("body = %q, want frontmatter 제거 + 앞뒤 공백 제거된 본문", got)
	}
}

func TestBodySource_CacheHitWithinTTL(t *testing.T) {
	f := &fakeFetcher{body: validFile}
	s, clock := newTestSource(f, BodyOptions{TTL: time.Hour})
	r := ref("content/0001-a.md", "sha1")

	for range 3 {
		if _, err := s.Get(context.Background(), r); err != nil {
			t.Fatal(err)
		}
		clock.Advance(10 * time.Minute)
	}
	if n := f.callCount(); n != 1 {
		t.Errorf("GitHub 호출 %d회, want 1 (TTL 안에서는 캐시)", n)
	}
}

func TestBodySource_RefetchAfterTTL(t *testing.T) {
	f := &fakeFetcher{body: validFile}
	s, clock := newTestSource(f, BodyOptions{TTL: time.Hour})
	r := ref("content/0001-a.md", "sha1")

	if _, err := s.Get(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Hour + time.Second)
	if _, err := s.Get(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if n := f.callCount(); n != 2 {
		t.Errorf("GitHub 호출 %d회, want 2 (TTL 지나면 다시 가져옴)", n)
	}
}

func TestBodySource_NewSHAInvalidatesCache(t *testing.T) {
	f := &fakeFetcher{body: validFile}
	s, _ := newTestSource(f, BodyOptions{TTL: time.Hour})

	if _, err := s.Get(context.Background(), ref("content/0001-a.md", "sha-old")); err != nil {
		t.Fatal(err)
	}
	f.set("---\ntitle: \"t\"\npreview: \"p\"\n---\n\n고친 본문\n", nil)
	got, err := s.Get(context.Background(), ref("content/0001-a.md", "sha-new"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "고친 본문" {
		t.Errorf("body = %q, want SHA가 바뀌면 TTL 안이어도 새 본문", got)
	}
	if n := f.callCount(); n != 2 {
		t.Errorf("GitHub 호출 %d회, want 2", n)
	}
}

func TestBodySource_ConcurrentRequestsShareOneFetch(t *testing.T) {
	f := &fakeFetcher{body: validFile, release: make(chan struct{}), started: make(chan struct{})}
	s, _ := newTestSource(f, BodyOptions{})
	r := ref("content/0001-a.md", "sha1")

	const n = 20
	var entered, done sync.WaitGroup
	entered.Add(n)
	done.Add(n)
	results := make([]string, n)
	errs := make([]error, n)
	for i := range n {
		go func(i int) {
			defer done.Done()
			entered.Done()
			results[i], errs[i] = s.Get(context.Background(), r)
		}(i)
	}
	entered.Wait()
	<-f.started
	time.Sleep(50 * time.Millisecond) // 나머지 호출이 대기열에 들어갈 시간을 준다
	close(f.release)
	done.Wait()

	for i := range n {
		if errs[i] != nil || results[i] != "본문입니다." {
			t.Fatalf("호출자 %d: body=%q err=%v", i, results[i], errs[i])
		}
	}
	if c := f.callCount(); c != 1 {
		t.Errorf("GitHub 호출 %d회, want 1 (동시 요청은 합쳐져야 함)", c)
	}
}

func TestBodySource_ServesStaleOnTransientError(t *testing.T) {
	f := &fakeFetcher{body: validFile}
	s, clock := newTestSource(f, BodyOptions{TTL: time.Hour, StaleTTL: 24 * time.Hour})
	r := ref("content/0001-a.md", "sha1")

	if _, err := s.Get(context.Background(), r); err != nil {
		t.Fatal(err)
	}

	f.set("", errors.New("raw status 503"))
	clock.Advance(2 * time.Hour) // TTL은 지났지만 StaleTTL 안
	got, err := s.Get(context.Background(), r)
	if err != nil {
		t.Fatalf("일시 장애에는 만료된 캐시를 줘야 함: %v", err)
	}
	if got != "본문입니다." {
		t.Errorf("body = %q", got)
	}

	clock.Advance(23 * time.Hour) // 처음 가져온 뒤 25시간: StaleTTL 초과
	if _, err := s.Get(context.Background(), r); err == nil {
		t.Error("StaleTTL을 넘긴 캐시는 주면 안 됨")
	}
}

func TestBodySource_NotFoundDropsStale(t *testing.T) {
	f := &fakeFetcher{body: validFile}
	s, clock := newTestSource(f, BodyOptions{TTL: time.Hour, StaleTTL: 24 * time.Hour})
	r := ref("content/0001-a.md", "sha1")

	if _, err := s.Get(context.Background(), r); err != nil {
		t.Fatal(err)
	}

	f.set("", fmt.Errorf("%w: content/0001-a.md", ErrNotFound))
	clock.Advance(2 * time.Hour)
	if _, err := s.Get(context.Background(), r); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound (삭제된 글에 옛 본문을 주면 안 됨)", err)
	}

	// 캐시에서도 지워졌는지: 이후 일시 장애가 나도 옛 본문이 되살아나면 안 된다.
	f.set("", errors.New("raw status 503"))
	if _, err := s.Get(context.Background(), r); err == nil {
		t.Error("삭제 확인된 글이 stale로 되살아남")
	}
}

func TestBodySource_InvalidFileIsNotCached(t *testing.T) {
	f := &fakeFetcher{body: "frontmatter 없는 파일"}
	s, _ := newTestSource(f, BodyOptions{})
	r := ref("content/0001-a.md", "sha1")

	for range 2 {
		if _, err := s.Get(context.Background(), r); err == nil {
			t.Fatal("frontmatter가 없으면 오류여야 함")
		}
	}
	if n := f.callCount(); n != 2 {
		t.Errorf("GitHub 호출 %d회, want 2 (실패는 캐시하지 않음)", n)
	}
}

func TestBodySource_EvictsOldestWhenOverMax(t *testing.T) {
	f := &fakeFetcher{body: validFile}
	s, clock := newTestSource(f, BodyOptions{MaxEntries: 2})
	a, b, c := ref("content/0001-a.md", "s"), ref("content/0002-b.md", "s"), ref("content/0003-c.md", "s")

	for _, r := range []BodyRef{a, b, c} {
		if _, err := s.Get(context.Background(), r); err != nil {
			t.Fatal(err)
		}
		clock.Advance(time.Second)
	}
	if n := f.callCount(); n != 3 {
		t.Fatalf("GitHub 호출 %d회, want 3", n)
	}

	if _, err := s.Get(context.Background(), c); err != nil { // 최근 것은 캐시에 남아 있음
		t.Fatal(err)
	}
	if n := f.callCount(); n != 3 {
		t.Errorf("c는 캐시 적중이어야 함 (호출 %d회)", n)
	}
	if _, err := s.Get(context.Background(), a); err != nil { // 가장 오래된 a는 밀려남
		t.Fatal(err)
	}
	if n := f.callCount(); n != 4 {
		t.Errorf("a는 밀려나서 다시 가져와야 함 (호출 %d회)", n)
	}
}

func TestBodySource_CallerCancelDoesNotFailOthers(t *testing.T) {
	f := &fakeFetcher{body: validFile, release: make(chan struct{}), started: make(chan struct{})}
	s, _ := newTestSource(f, BodyOptions{})
	r := ref("content/0001-a.md", "sha1")

	// 첫 요청자가 대기 중 연결을 끊는다.
	cancelCtx, cancel := context.WithCancel(context.Background())
	firstErr := make(chan error, 1)
	go func() {
		_, err := s.Get(cancelCtx, r)
		firstErr <- err
	}()
	<-f.started

	// 두 번째 요청자는 같은 요청을 기다린다.
	type result struct {
		body string
		err  error
	}
	second := make(chan result, 1)
	go func() {
		b, err := s.Get(context.Background(), r)
		second <- result{b, err}
	}()
	time.Sleep(50 * time.Millisecond)

	cancel()
	if err := <-firstErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("첫 요청자 err = %v, want context.Canceled", err)
	}

	close(f.release)
	got := <-second
	if got.err != nil || got.body != "본문입니다." {
		t.Errorf("두 번째 요청자: body=%q err=%v (첫 요청자가 끊어도 영향받으면 안 됨)", got.body, got.err)
	}
}
