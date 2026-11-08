-- 새 질문 알림을 보낸 시각. NULL이면 아직 알림 대상.
-- 이미 저장된 행은 알림을 받은 것으로 채움(배포 당일 지난 질문으로 메일이 나가지 않게).
ALTER TABLE daily_question ADD COLUMN notified_at TIMESTAMP;
UPDATE daily_question SET notified_at = CURRENT_TIMESTAMP;
