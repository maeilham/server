-- 오늘의 질문 저장.
-- 메일 발송과 무관하게 그날 그 repo의 질문을 정해 두고, 웹 API와 메일이 같은 행을 읽음.
--
-- day는 서비스 시간대(MAEILHAM_TZ)의 날짜 문자열 'YYYY-MM-DD'임(사전순 비교가 날짜순과 같음).
-- review가 1이면 이미 나온 글을 다시 보여 주는 복습이고, 0이면 처음 나오는 새 글
CREATE TABLE daily_question (
    day        TEXT    NOT NULL,
    repo_slug  TEXT    NOT NULL,
    content_id TEXT    NOT NULL,
    review     INTEGER NOT NULL DEFAULT 0 CHECK (review IN (0, 1)),
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (day, repo_slug),
    FOREIGN KEY (repo_slug, content_id) REFERENCES contents(repo_slug, content_id)
);

-- 글의 마지막으로 나온 날(복습 후보 정렬)을 구하는 조회용 인덱스.
CREATE INDEX idx_daily_question_content ON daily_question(repo_slug, content_id, day);
