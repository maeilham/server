package store

import (
	"context"
	"database/sql"
	"fmt"
)

type DailyQuestion struct {
	Day       string // 서비스 시간대(MAEILHAM_TZ)의 날짜 'YYYY-MM-DD'
	RepoSlug  string
	ContentID string
	Review    bool // 이미 나온 글을 다시 보여 주는 복습이면 true, 처음 나오는 새 글이면 false
}

// DailyQuestionRepository는 repo별 오늘의 질문을 저장하고 조회해요
type DailyQuestionRepository interface {
	// Get은 (day, repoSlug)에 저장된 질문 반환
	Get(ctx context.Context, day, repoSlug string) (*DailyQuestion, error)

	// Ensure는 (day, repoSlug)의 질문을 돌려주고, 아직 없으면 정해서 저장함. (멱등)
	Ensure(ctx context.Context, day, repoSlug string) (*DailyQuestion, error)
}

type sqlDailyQuestionStore struct{ db *sql.DB }

func NewDailyQuestionStore(db *sql.DB) DailyQuestionRepository {
	return &sqlDailyQuestionStore{db: db}
}

func (s *sqlDailyQuestionStore) Get(ctx context.Context, day, repoSlug string) (*DailyQuestion, error) {
	var q DailyQuestion
	var review int
	err := s.db.QueryRowContext(ctx,
		`SELECT day, repo_slug, content_id, review FROM daily_question WHERE day = ? AND repo_slug = ?`,
		day, repoSlug,
	).Scan(&q.Day, &q.RepoSlug, &q.ContentID, &review)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get daily question %s/%s: %w", day, repoSlug, err)
	}
	q.Review = review == 1
	return &q, nil
}

func (s *sqlDailyQuestionStore) Ensure(ctx context.Context, day, repoSlug string) (*DailyQuestion, error) {
	cur, err := s.Get(ctx, day, repoSlug)
	if err != nil {
		return nil, err
	}
	if cur != nil {
		live, err := s.contentIsLive(ctx, repoSlug, cur.ContentID)
		if err != nil {
			return nil, err
		}
		if live {
			return cur, nil
		}
	}

	contentID, review, ok, err := s.choose(ctx, day, repoSlug)
	if err != nil || !ok {
		return nil, err
	}

	reviewInt := 0
	if review {
		reviewInt = 1
	}
	if cur == nil {
		// 동시에 정하려는 다른 요청이 먼저 저장했으면 무시되고, 아래 조회가 그 행을 돌려줌.
		_, err = s.db.ExecContext(ctx,
			`INSERT OR IGNORE INTO daily_question (day, repo_slug, content_id, review) VALUES (?, ?, ?, ?)`,
			day, repoSlug, contentID, reviewInt)
	} else {
		// 삭제된 글을 가리키는 행을 갱신함. 그 사이에 다른 요청이 이미 바꿨으면 조건이 안 맞아 무시됨.
		_, err = s.db.ExecContext(ctx,
			`UPDATE daily_question SET content_id = ?, review = ? WHERE day = ? AND repo_slug = ? AND content_id = ?`,
			contentID, reviewInt, day, repoSlug, cur.ContentID)
	}
	if err != nil {
		return nil, fmt.Errorf("save daily question %s/%s: %w", day, repoSlug, err)
	}
	return s.Get(ctx, day, repoSlug)
}

// choose는 day의 질문 후보를 정함. day보다 이전에 나온 기록만 보므로 같은 날의 행은 영향을 주지 않음.
func (s *sqlDailyQuestionStore) choose(ctx context.Context, day, repoSlug string) (contentID string, review, ok bool, err error) {
	var lastDay sql.NullString
	err = s.db.QueryRowContext(ctx, `
		SELECT c.content_id, last.day
		  FROM contents c
		  LEFT JOIN (
		      SELECT content_id, MAX(day) AS day
		        FROM daily_question
		       WHERE repo_slug = ? AND day < ?
		       GROUP BY content_id
		  ) last ON last.content_id = c.content_id
		 WHERE c.repo_slug = ? AND c.deleted_at IS NULL
		 ORDER BY last.day IS NOT NULL, last.day, c.content_id
		 LIMIT 1`,
		repoSlug, day, repoSlug,
	).Scan(&contentID, &lastDay)
	if err == sql.ErrNoRows {
		return "", false, false, nil
	}
	if err != nil {
		return "", false, false, fmt.Errorf("choose daily question %s/%s: %w", day, repoSlug, err)
	}
	return contentID, lastDay.Valid, true, nil
}

func (s *sqlDailyQuestionStore) contentIsLive(ctx context.Context, repoSlug, contentID string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM contents WHERE repo_slug = ? AND content_id = ? AND deleted_at IS NULL`,
		repoSlug, contentID,
	).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check content %s/%s: %w", repoSlug, contentID, err)
	}
	return true, nil
}
