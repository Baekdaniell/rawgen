package verify

import (
	"testing"
	"time"
)

// hourlyInWindow — 제품 규칙: H시 행은 (H+1):05+여유(10분) 이후 존재,
// 익일 00:05 truncate로 전날 00~22시 소멸, 전날 23시만 잔존.
func TestHourlyInWindow(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Seoul")
	now := time.Date(2026, 8, 12, 14, 30, 0, 0, loc) // 오늘 14:30

	cases := []struct {
		date string
		hour int
		want bool
		desc string
	}{
		{"2026-08-12", 12, true, "오늘 12시 — 13:15 생성 완료분"},
		{"2026-08-12", 13, true, "오늘 13시 — 14:15 생성 완료, 14:30이면 창 안"},
		{"2026-08-12", 14, false, "오늘 14시 — 진행 중, 미생성"},
		{"2026-08-12", 23, false, "오늘 23시 — 미래"},
		{"2026-08-11", 23, true, "전날 23시 — 오늘 00:05 재생성, 잔존"},
		{"2026-08-11", 22, false, "전날 22시 — 00:05 truncate로 소멸"},
		{"2026-08-11", 0, false, "전날 00시 — 소멸"},
		{"2026-08-10", 23, false, "그제 23시 — 어제 00:05 truncate로 소멸"},
		{"잘못된날짜", 5, false, "파싱 실패는 창 밖 취급"},
	}
	for _, c := range cases {
		got := hourlyInWindow(c.date, c.hour, now, loc)
		if got != c.want {
			t.Errorf("%s (date=%s hour=%d): got %v want %v", c.desc, c.date, c.hour, got, c.want)
		}
	}

	// 자정 직후 경계: 00:10 — 전날 23시는 아직 미생성(생성 완료 기대 시각 00:15)
	early := time.Date(2026, 8, 12, 0, 10, 0, 0, loc)
	if hourlyInWindow("2026-08-11", 23, early, loc) {
		t.Errorf("00:10에 전날 23시는 아직 미생성이어야 함(00:15부터)")
	}
	// 00:20 — 생성 완료 창 안
	after := time.Date(2026, 8, 12, 0, 20, 0, 0, loc)
	if !hourlyInWindow("2026-08-11", 23, after, loc) {
		t.Errorf("00:20에 전날 23시는 창 안이어야 함")
	}
	// 00:20에 전날 22시는 truncate로 소멸
	if hourlyInWindow("2026-08-11", 22, after, loc) {
		t.Errorf("00:20에 전날 22시는 소멸이어야 함")
	}
}
