-- "오늘 그 repo에서 실제로 나간 글"을 찾는 조회(LastDeliveredContentID)를 없앴으므로, 그 조회용 인덱스(003)도 지움.
-- 오늘의 질문은 daily_question(005)에서 읽고, delivery_log는 메일 중복 방지와 이력 용도만 남음.
DROP INDEX IF EXISTS idx_delivery_log_repo_sent;
