ALTER TABLE contents ADD COLUMN github_sha TEXT;

CREATE INDEX idx_contents_github_sha ON contents(repo_slug, github_sha);
