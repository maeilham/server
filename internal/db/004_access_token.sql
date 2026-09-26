-- 구독자마다 하나의 개인 링크 토큰. 메일로 받은 링크(`<웹>/#t=<토큰>`)로 들어오면 그 사람으로 식별되어
-- 푼 문제 기록이 저장된다. 무작위 256비트(16진수 64자)이고 이메일이나 다른 값에서 유도하지 않는다.
-- 해지 링크의 HMAC 토큰과는 별개다(접근 토큰이 새도 해지나 이메일 확인에는 쓸 수 없게 분리).
--
-- 이미 있는 구독자에게는 아래에서 바로 채운다. randomblob은 행마다 다시 계산된다.
ALTER TABLE subscribers ADD COLUMN access_token TEXT;
UPDATE subscribers SET access_token = lower(hex(randomblob(32))) WHERE access_token IS NULL;
CREATE UNIQUE INDEX idx_subscribers_access_token ON subscribers(access_token);
