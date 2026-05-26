package delivery

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// AdvanceRotation bumps rotation_count and sets sent_at on every (repo, content)
// pair that received at least one delivery on `day`. Called once after the
// daily send batch finishes so that:
//
//   - 발송 도중 cron이 죽으면 advance가 안 일어남 -> 다음 날 같은 콘텐츠 재시도
//   - 발송이 성공적으로 끝났을 때만 다음 콘텐츠로 진행
//   - 한 번 발송된 콘텐츠는 정확히 1 카운트만 advance (사용자 N명 받아도 +1)
//   - 같은 날 두 번 호출돼도 +1만 (멱등: sent_at 날짜로 가드)
//
// Returns the number of content rows updated.
func AdvanceRotation(ctx context.Context, db *sql.DB, day time.Time) (int64, error) {
	dayStr := day.Format("2006-01-02")
	res, err := db.ExecContext(ctx, `
		UPDATE contents
		   SET sent_at        = ?,
		       rotation_count = rotation_count + 1
		 WHERE (repo_slug, content_id) IN (
		     SELECT DISTINCT repo_slug, content_id
		       FROM delivery_log
		      WHERE date(sent_at) = date(?)
		 )
		   AND (sent_at IS NULL OR date(sent_at) != date(?))`,
		day.UTC().Format("2006-01-02 15:04:05"), dayStr, dayStr,
	)
	if err != nil {
		return 0, fmt.Errorf("advance rotation: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	return n, nil
}
