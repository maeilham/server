# maeilham/server

매일함 백엔드 서버. HTTP API, SSH 터미널, 콘텐츠 동기화, 메일 발송을 담당합니다.

## 구조

```text
cmd/
  server/   HTTP + SSH 서버 (장기 실행 데몬)
  cron/     배치 작업 CLI (sync, send-daily, summarize 등)

internal/
  content/    GitHub repo에서 콘텐츠 동기화
  delivery/   구독자별 콘텐츠 선택 및 발송
  github/     GitHub App 인증, Discussion 생성/수정
  http/       HTTP API 라우터 및 핸들러
  mail/       메일 렌더링 및 발송 (Resend)
  subscriber/ 구독자 저장소
  terminal/   SSH 터미널 핸들러 및 서비스
```

## 빠른 시작

```bash
# 환경변수 설정
cp .env.example .env  # 값 채우기

# 서버 실행
source .env && go run ./cmd/server

# SSH 접속
ssh -p 2222 localhost
```

## 환경변수

| 변수                              | 필수 | 설명                                     |
| --------------------------------- | ---- | ---------------------------------------- |
| `MAEILHAM_DB`                     | -    | SQLite 경로 (기본: `./data/maeilham.db`) |
| `MAEILHAM_HTTP_ADDR`              | -    | HTTP 주소 (기본: `:8080`)                |
| `MAEILHAM_SSH_ADDR`               | -    | SSH 주소 (기본: `:2222`)                 |
| `MAEILHAM_BASE_URL`               | -    | 웹 프론트 URL                            |
| `MAEILHAM_API_URL`                | -    | API 서버 URL                             |
| `MAEILHAM_SECRET`                 | ✓    | 토큰 서명 키                             |
| `MAEILHAM_RESEND_API_KEY`         | ✓    | Resend API 키                            |
| `MAEILHAM_MAIL_FROM_EMAIL`        | ✓    | 발신 이메일 주소                         |
| `MAEILHAM_GITHUB_APP_ID`          | ✓    | GitHub App ID                            |
| `MAEILHAM_GITHUB_APP_PEM`         | ✓    | GitHub App PEM 경로                      |
| `MAEILHAM_GITHUB_INSTALLATION_ID` | ✓    | GitHub App 설치 ID                       |
| `MAEILHAM_GITHUB_TOKEN`           | -    | GitHub PAT (콘텐츠 sync용)               |

## cron 커맨드

```bash
# repo 관리
cron repo add --slug <slug> --url <github_url> --name <name>
cron repo list
cron repo deactivate --slug <slug>

# 콘텐츠 동기화
cron sync [--repo <slug>]

# 메일 발송
cron send-daily [--dry-run] [--date YYYY-MM-DD]
cron send-test --to <email>

# Discussion 댓글 AI 요약
cron summarize --repo <slug> --content-id <id> [--ai-cmd "claude --print"] [--yes]

# 유틸
cron gen-link --email <email> [--type unsubscribe|confirm]
```

## 빌드

```bash
make all       # server + cron 빌드 (linux/amd64)
make server
make cron
```
