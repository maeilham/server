-- "오늘 그 repo에서 실제로 나간 글"을 찾는 조회(repo_slug + sent_at 구간, 최신 1개)용 인덱스.
-- 없으면 그 repo의 발송 기록 전체를 훑어 정렬하게 되어, 발송 기록이 쌓일수록 느려진다.
CREATE INDEX idx_delivery_log_repo_sent ON delivery_log(repo_slug, sent_at);
