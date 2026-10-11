package delivery

import "time"

// ServiceDay는 t가 속한 "서비스의 하루"를 loc 시간대(MAEILHAM_TZ)의 [00:00, 다음날 00:00) 시각 구간으로 돌려준다.
//
// 오늘의 질문(웹), 중복 발송 판단, 로테이션 넘김은 모두 이 구간을 쓴다. 하루를 날짜 문자열로 비교하면
// 시간대에 따라 하루가 바뀌는 시각이 달라져서(예: UTC는 한국 09:00) 같은 한국 날짜에 2번 발송되는 일이
// 생긴다. 구간의 끝은 AddDate로 구해서, 서머타임이 있는 시간대에서도 하루 길이(23/25시간)가 맞다.
func ServiceDay(t time.Time, loc *time.Location) (start, end time.Time) {
	l := t.In(loc)
	start = time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, loc)
	return start, start.AddDate(0, 0, 1)
}
