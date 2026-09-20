package content

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// BodyRef는 본문을 가져올 콘텐츠 하나를 가리킨다.
// 호출자가 repos(github_url)와 contents(body_path, github_sha)를 조합해 만든다.
type BodyRef struct {
	Owner string
	Repo  string
	Path  string // 예: content/0001-slug.md
	SHA   string // contents.github_sha. 캐시 키에 포함되어, sync로 SHA가 바뀌면 자동으로 무효화된다
}

func (r BodyRef) key() string {
	return r.Owner + "/" + r.Repo + "/" + r.Path + "@" + r.SHA
}

// BodySource는 콘텐츠의 마크다운 본문(frontmatter 제외)을 돌려준다.
// 원본은 항상 GitHub repo이고, 구현체는 그것을 가져오는 방식만 다르다.
// 나중에 DB 저장 방식으로 바꿔도 호출부는 그대로 쓸 수 있도록 인터페이스로 둔다.
type BodySource interface {
	Get(ctx context.Context, ref BodyRef) (string, error)
}

// RawFetcher는 GitHub에서 파일 원문을 가져온다. *GitHubClient가 구현한다.
type RawFetcher interface {
	FetchRaw(ctx context.Context, owner, repo, ref, path string) ([]byte, error)
}

// BodyOptions는 CachedBodySource의 동작을 조절한다. 0값은 기본값으로 대체된다.
type BodyOptions struct {
	Ref          string        // 가져올 브랜치. 기본 "main"
	TTL          time.Duration // 이 시간 안에는 GitHub를 다시 부르지 않는다. 기본 1시간
	StaleTTL     time.Duration // GitHub 장애 시 이 시간 안의 만료된 캐시는 그대로 제공한다. 기본 24시간
	MaxEntries   int           // 캐시에 두는 최대 글 수. 기본 256
	FetchTimeout time.Duration // GitHub 요청 하나의 상한. 기본 20초
	Logger       *slog.Logger
}

func (o BodyOptions) withDefaults() BodyOptions {
	if o.Ref == "" {
		o.Ref = "main"
	}
	if o.TTL <= 0 {
		o.TTL = time.Hour
	}
	if o.StaleTTL <= 0 {
		o.StaleTTL = 24 * time.Hour
	}
	if o.MaxEntries <= 0 {
		o.MaxEntries = 256
	}
	if o.FetchTimeout <= 0 {
		o.FetchTimeout = 20 * time.Second
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return o
}

// CachedBodySource는 GitHub에서 본문을 가져오고 메모리에 잠시 들고 있는다.
//
//   - 캐시 키에 SHA가 들어가므로 글이 바뀌어 sync되면 새 키로 다시 가져온다.
//   - 같은 글의 동시 요청은 GitHub 요청 하나로 합친다(아침 발송 직후 몰림 대비).
//   - GitHub 요청이 일시적으로 실패하면 StaleTTL 안의 만료된 캐시를 대신 준다.
//     단, 파일이 없다는 응답(ErrNotFound)은 삭제된 글이므로 옛 본문을 주지 않는다.
//   - 프로세스 메모리에만 있어 재시작하면 비워진다.
type CachedBodySource struct {
	fetcher RawFetcher
	opts    BodyOptions
	now     func() time.Time // 테스트에서 시간을 제어하기 위한 주입점

	mu       sync.Mutex
	entries  map[string]*bodyEntry
	inflight map[string]*bodyCall
}

type bodyEntry struct {
	body      string
	fetchedAt time.Time
}

// bodyCall은 진행 중인 GitHub 요청 하나다. 기다리는 모든 호출자가 결과를 공유한다.
type bodyCall struct {
	done chan struct{}
	body string
	err  error
}

func NewCachedBodySource(fetcher RawFetcher, opts BodyOptions) *CachedBodySource {
	return &CachedBodySource{
		fetcher:  fetcher,
		opts:     opts.withDefaults(),
		now:      time.Now,
		entries:  make(map[string]*bodyEntry),
		inflight: make(map[string]*bodyCall),
	}
}

func (s *CachedBodySource) Get(ctx context.Context, ref BodyRef) (string, error) {
	key := ref.key()

	s.mu.Lock()
	if e := s.entries[key]; e != nil && s.now().Sub(e.fetchedAt) < s.opts.TTL {
		body := e.body
		s.mu.Unlock()
		return body, nil
	}
	call, ok := s.inflight[key]
	if !ok {
		call = &bodyCall{done: make(chan struct{})}
		s.inflight[key] = call
		go s.fetch(key, ref, call)
	}
	s.mu.Unlock()

	select {
	case <-call.done:
		return call.body, call.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// fetch는 호출자의 ctx와 분리되어 실행된다. 요청한 사람 하나가 연결을 끊어도
// 같은 결과를 기다리는 다른 사람의 요청까지 실패하면 안 되기 때문이다.
func (s *CachedBodySource) fetch(key string, ref BodyRef, call *bodyCall) {
	ctx, cancel := context.WithTimeout(context.Background(), s.opts.FetchTimeout)
	defer cancel()
	body, err := s.load(ctx, ref)

	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.inflight, key)

	switch {
	case err == nil:
		s.entries[key] = &bodyEntry{body: body, fetchedAt: s.now()}
		s.evictLocked()
		call.body = body
	case errors.Is(err, ErrNotFound):
		delete(s.entries, key)
		call.err = err
	default:
		if e := s.entries[key]; e != nil && s.now().Sub(e.fetchedAt) < s.opts.StaleTTL {
			s.opts.Logger.Warn("github fetch failed; serving stale body",
				"path", ref.Path, "age", s.now().Sub(e.fetchedAt).Round(time.Second), "err", err)
			call.body = e.body
		} else {
			call.err = err
		}
	}
	close(call.done)
}

func (s *CachedBodySource) load(ctx context.Context, ref BodyRef) (string, error) {
	raw, err := s.fetcher.FetchRaw(ctx, ref.Owner, ref.Repo, s.opts.Ref, ref.Path)
	if err != nil {
		return "", err
	}
	parsed, err := Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", ref.Path, err)
	}
	return parsed.Body, nil
}

// evictLocked는 상한을 넘으면 가장 오래전에 가져온 글부터 지운다. s.mu를 잡고 호출해야 한다.
func (s *CachedBodySource) evictLocked() {
	for len(s.entries) > s.opts.MaxEntries {
		var oldestKey string
		var oldest time.Time
		for k, e := range s.entries {
			if oldestKey == "" || e.fetchedAt.Before(oldest) {
				oldestKey, oldest = k, e.fetchedAt
			}
		}
		delete(s.entries, oldestKey)
	}
}
