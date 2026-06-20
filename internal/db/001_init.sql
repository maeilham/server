-- Subscribers (구독자)
CREATE TABLE subscribers (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    email           TEXT    NOT NULL UNIQUE,
    github_username TEXT,
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    confirmed_at    TIMESTAMP,
    paused_at       TIMESTAMP,
    unsubscribed_at TIMESTAMP
);

-- Content repos (운영자가 등록한 콘텐츠 저장소)
CREATE TABLE repos (
    slug         TEXT    PRIMARY KEY,           -- 'backend-interview'
    github_url   TEXT    NOT NULL,              -- 'https://github.com/maeilham/backend-interview'
    display_name TEXT    NOT NULL,              -- '백엔드 면접 질문'
    description  TEXT,
    active                 INTEGER NOT NULL DEFAULT 1,
    discussion_category_id TEXT,
    created_at             TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Subscriptions (구독자 × repo, 가중치 포함)
CREATE TABLE subscriptions (
    subscriber_id INTEGER NOT NULL REFERENCES subscribers(id) ON DELETE CASCADE,
    repo_slug     TEXT    NOT NULL REFERENCES repos(slug) ON DELETE CASCADE,
    weight        INTEGER NOT NULL DEFAULT 3 CHECK (weight BETWEEN 1 AND 5),
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (subscriber_id, repo_slug)
);

-- Contents (repo에서 sync된 콘텐츠 인덱스)
CREATE TABLE contents (
    repo_slug      TEXT    NOT NULL REFERENCES repos(slug) ON DELETE CASCADE,
    content_id     TEXT    NOT NULL,             -- '0001' (파일명 4자리 숫자)
    title          TEXT    NOT NULL,
    preview        TEXT    NOT NULL,
    tags           TEXT,                          -- JSON array
    source_url     TEXT,                          -- frontmatter.source.url
    source_author  TEXT,
    body_path      TEXT    NOT NULL,             -- 'content/0001-scale-up-vs-scale-out.md'
    github_sha     TEXT,                          -- GitHub blob sha (변경 감지용)
    sent_at        TIMESTAMP,                    -- 마지막 발송 시각
    rotation_count INTEGER NOT NULL DEFAULT 0,
    discussion_url     TEXT,
    discussion_node_id TEXT,
    synced_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at     TIMESTAMP,
    PRIMARY KEY (repo_slug, content_id)
);

CREATE INDEX idx_contents_pickorder ON contents(repo_slug, rotation_count, content_id)
    WHERE deleted_at IS NULL;
CREATE INDEX idx_contents_github_sha ON contents(repo_slug, github_sha);

-- Delivery log (사용자별 발송 이력)
CREATE TABLE delivery_log (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    subscriber_id  INTEGER NOT NULL REFERENCES subscribers(id) ON DELETE CASCADE,
    repo_slug      TEXT    NOT NULL,
    content_id     TEXT    NOT NULL,
    channel        TEXT    NOT NULL DEFAULT 'email',
    sent_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    opened_at      TIMESTAMP,
    clicked_at     TIMESTAMP,
    FOREIGN KEY (repo_slug, content_id) REFERENCES contents(repo_slug, content_id)
);

CREATE INDEX idx_delivery_log_subscriber ON delivery_log(subscriber_id, sent_at);
CREATE INDEX idx_delivery_log_content    ON delivery_log(repo_slug, content_id);
