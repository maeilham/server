package main

import (
	"testing"
	"time"
)

func TestSendDay(t *testing.T) {
	kst := time.FixedZone("KST", 9*3600)
	now := time.Date(2026, 5, 27, 1, 0, 0, 0, time.UTC)

	// --date가 없으면 지금 시각 그대로다(하루는 Loc이 정한다)
	got, err := sendDay(now, "", kst)
	if err != nil || !got.Equal(now) {
		t.Errorf("no --date: got %v err %v, want now", got, err)
	}
	// --date는 서비스 시간대의 그날 0시다. UTC 0시(= 한국 09:00)로 읽으면 안 된다
	got, err = sendDay(now, "2026-05-28", kst)
	want := time.Date(2026, 5, 27, 15, 0, 0, 0, time.UTC)
	if err != nil || !got.Equal(want) {
		t.Errorf("--date 2026-05-28: got %s err %v, want %s (00:00 KST)", got.UTC().Format(time.RFC3339), err, want.Format(time.RFC3339))
	}
	if _, err := sendDay(now, "not-a-date", kst); err == nil {
		t.Error("invalid --date must be an error")
	}
}
