# maeilham/server

매일함 백엔드 서버. HTTP API, SSH 터미널, 콘텐츠 동기화, 메일 발송을 담당합니다.

## 구조

단일 바이너리(`maeilham`)이고, 서브커맨드로 서버와 배치 작업을 나눕니다.

```text
cmd/
  maeilham/   진입점 (serve, sync, send-daily, send-test, repo, gen-link)

internal/
  content/    GitHub repo에서 콘텐츠 동기화, 본문(마크다운) 조회와 메모리 캐시
  db/         SQLite 연결, 마이그레이션 (*.sql)
  delivery/   구독자별 콘텐츠 선택 및 발송
  github/     GitHub App 인증, Discussion 생성/수정
  http/       HTTP API 라우터 및 핸들러
  mail/       메일 렌더링 및 발송 (Resend)
  store/      DB 접근 (구독자, repo, 콘텐츠, 발송 이력)
  subscriber/ 구독 신청/확인/해지 서비스
  terminal/   SSH 터미널 핸들러 및 서비스
```

## 빠른 시작

**반드시 `server/` 디렉터리에서 실행하세요.** DB 기본 경로(`./data/maeilham.db`)가 상대 경로입니다.

```bash
cd server

# 환경변수 설정
cp .env.example .env  # 값 채우기

# 서버 실행 (HTTP :8080, SSH :2222)
set -a && source .env && set +a
go run ./cmd/maeilham serve

# SSH 접속
ssh -p 2222 localhost
```

- `.env.example`은 `KEY=value` 형식이라 `source`만 하면 자식 프로세스(`go run`)에 전달되지 않습니다. 위처럼 `set -a`로 감싸거나, `.env`의 각 줄 앞에 `export`를 붙이세요.
- 이미 8080 포트에서 다른 서버가 돌고 있으면 `address already in use`로 종료됩니다. 먼저 종료하세요.

### 콘텐츠를 넣고 API 확인하기

처음에는 DB가 비어 있어서 콘텐츠 API가 404를 돌려줍니다. 서버를 띄운 뒤 다른 터미널에서:

```bash
cd server && set -a && source .env && set +a

# 1) 콘텐츠 repo 등록
go run ./cmd/maeilham repo add --slug backend-ops --url https://github.com/<owner>/backend-ops --name "백엔드 · 인프라"

# 2) GitHub에서 콘텐츠 목록을 가져와 DB에 저장
go run ./cmd/maeilham sync --repo backend-ops

# 3) 어떤 글이 들어왔는지 확인
sqlite3 -readonly data/maeilham.db "select repo_slug, content_id, title from contents;"

# 4) API 호출
curl -s localhost:8080/api/contents/backend-ops/0001 | jq
```

## HTTP API

| 메서드 | 경로 | 설명 |
| ------ | ---- | ---- |
| GET | `/healthz` | 상태 확인 |
| POST | `/api/subscribe` | 구독 신청 (확인 메일 발송) |
| GET | `/api/confirm?token=` | 구독 확인 후 웹으로 리다이렉트 |
| POST | `/api/unsubscribe?token=` | 구독 해지 |
| GET | `/api/contents?limit=N` | 콘텐츠 목록 (본문 제외, 작성일 최신순, 기본 50 최대 100) |
| GET | `/api/contents/{repo}/{id}` | 콘텐츠 상세 (메타데이터 + 마크다운 본문) |
| GET | `/ws/terminal` | 웹 터미널용 WebSocket 브리지 |

`content_id`는 repo 안에서만 유일하므로 콘텐츠는 항상 `{repo}/{id}` 쌍으로 가리킵니다.
본문은 요청 시 GitHub에서 가져와 메모리에 캐시합니다(원본은 항상 GitHub repo).

## 환경변수

| 변수                              | 필수 | 설명                                     |
| --------------------------------- | ---- | ---------------------------------------- |
| `MAEILHAM_DB`                     | -    | SQLite 경로 (기본: `./data/maeilham.db`) |
| `MAEILHAM_HTTP_ADDR`              | -    | HTTP 주소 (기본: `:8080`)                |
| `MAEILHAM_SSH_ADDR`               | -    | SSH 주소 (기본: `:2222`)                 |
| `MAEILHAM_BASE_URL`               | -    | 웹 프론트 URL                            |
| `MAEILHAM_API_URL`                | -    | API 서버 URL                             |
| `MAEILHAM_SECRET`                 | ✓    | 토큰 서명 키 (개발 기본값이 있지만 운영에서는 반드시 변경) |
| `MAEILHAM_RESEND_API_KEY`         | ✓    | Resend API 키                            |
| `MAEILHAM_MAIL_FROM_EMAIL`        | ✓    | 발신 이메일 주소                         |
| `MAEILHAM_GITHUB_APP_ID`          | ✓    | GitHub App ID                            |
| `MAEILHAM_GITHUB_APP_PEM`         | ✓    | GitHub App PEM 경로                      |
| `MAEILHAM_GITHUB_INSTALLATION_ID` | ✓    | GitHub App 설치 ID                       |
| `MAEILHAM_GITHUB_TOKEN`           | -    | GitHub PAT (콘텐츠 sync·본문 조회용, rate limit 완화와 private repo 접근. public repo만이면 생략 가능) |

## CLI 커맨드

`maeilham <command>` 형태입니다. 개발 중에는 `go run ./cmd/maeilham <command>`로 실행합니다.

```bash
# 서버
maeilham serve

# repo 관리
maeilham repo add --slug <slug> --url <github_url> --name <name>
maeilham repo list
maeilham repo deactivate --slug <slug>

# 콘텐츠 동기화
maeilham sync [--repo <slug>]

# 메일 발송
maeilham send-daily [--dry-run] [--date YYYY-MM-DD]
maeilham send-test --to <email>

# 유틸
maeilham gen-link --email <email> [--type unsubscribe|confirm]
```

전체 옵션은 `maeilham <command> --help`로 확인할 수 있습니다.

## 빌드와 테스트

```bash
make build   # bin/maeilham (linux/amd64, 배포용)
make test    # go test ./...
```

`make build`는 **리눅스용** 바이너리를 만듭니다(홈서버 배포용). 맥에서 로컬로 돌릴 때는 위의 `go run`을 쓰세요.
