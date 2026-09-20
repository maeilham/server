-- 글이 작성된 시각. GitHub에서 그 파일의 최초 커밋 시각을 sync가 채운다.
-- 발송 시각(sent_at)은 로테이션으로 덮어써지고 발송 전 글에는 없어서 목록에 쓰기에 맞지 않다.
-- NULL이면 아직 못 채운 것이고, 목록은 synced_at으로 대체해 보여준다. 다음 sync가 다시 시도한다.
ALTER TABLE contents ADD COLUMN authored_at TIMESTAMP;
