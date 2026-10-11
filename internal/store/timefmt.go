package store

// timeLayout은 sent_at 같은 시각 컬럼의 저장 형식이다(항상 UTC). 시각 구간을 같은 형식의 문자열로 바꿔서
// 비교해야 문자열 비교가 시간 비교가 된다.
const timeLayout = "2006-01-02 15:04:05"
