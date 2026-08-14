package e2e

import (
	"testing"
	"time"

	"oqt325/internal/model"
	"oqt325/internal/verify"
)

func mustLoc(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Seoul")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func previewFor(date string, hours ...int) *model.Preview {
	de := model.DayExpected{CheckpointID: 100, Date: date}
	for _, h := range hours {
		de.Hours = append(de.Hours, model.HourExpected{Date: date, Hour: h, Stats: model.Stats{Count: 60}})
	}
	return &model.Preview{Days: []model.DayExpected{de}}
}

func cellAt(cells []*Cell, hour int) *Cell {
	for _, c := range cells {
		if c.Hour == hour {
			return c
		}
	}
	return nil
}

// 아침 주입: 생성 시각((H+1):05)이 이미 지난 시간대는 skip, 남은 시간대만 wait.
// 백필은 그 시간대 hourly 행이 통째로 0일 때만 도므로(운영 환경은 타 cp 행이 있어
// 절대 0이 아님) 지난 시간대를 구제해주지 않는다.
func TestPlanCellsMorningInject(t *testing.T) {
	loc := mustLoc(t)
	inject := time.Date(2026, 8, 14, 9, 0, 0, 0, loc)
	cells := planCells(previewFor("2026-08-14", 3, 10, 23), inject, loc, false)

	// hour 3의 생성(04:05)은 이미 지남 → skip
	if c := cellAt(cells, 3); c.Status != StatusSkip {
		t.Errorf("hour 3 status = %s, want skip (04:05 생성이 이미 지남)", c.Status)
	}
	// hour 10은 11:05 생성 예정 → wait, due = 11:05+여유
	c10 := cellAt(cells, 10)
	if c10.Status != StatusWait {
		t.Errorf("hour 10 status = %s, want wait", c10.Status)
	}
	if !c10.dueAt.Equal(verify.HourlyDueAt("2026-08-14", 10, loc)) {
		t.Errorf("hour 10 due = %v, want 11:15", c10.dueAt)
	}
	// hour 23은 익일 00:05 재생성 후
	c23 := cellAt(cells, 23)
	if c23.Status != StatusWait || c23.dueAt.Day() != 15 {
		t.Errorf("hour 23 = %s due %v, want wait/next day", c23.Status, c23.dueAt)
	}
	// 일별 셀은 익일 00:05+여유
	d := cellAt(cells, -1)
	if d == nil || d.Status != StatusWait || d.dueAt.Day() != 15 {
		t.Fatalf("daily cell = %+v, want wait/next day", d)
	}
}

// 정오 주입: 경계 확인 — 생성 시각을 1분 넘긴 시간대는 skip, 아직 안 온 시간대는 wait.
func TestPlanCellsBoundary(t *testing.T) {
	loc := mustLoc(t)
	// 12:06 주입 → hour 11(12:05 생성)은 놓침, hour 12(13:05 생성)는 유효
	inject := time.Date(2026, 8, 14, 12, 6, 0, 0, loc)
	cells := planCells(previewFor("2026-08-14", 11, 12), inject, loc, true)
	if c := cellAt(cells, 11); c.Status != StatusSkip {
		t.Errorf("hour 11 status = %s, want skip (12:05 생성 직후 주입)", c.Status)
	}
	if c := cellAt(cells, 12); c.Status != StatusWait {
		t.Errorf("hour 12 status = %s, want wait", c.Status)
	}

	// 12:04 주입이면 hour 11은 아직 생성 전이라 유효
	cells = planCells(previewFor("2026-08-14", 11), inject.Add(-2*time.Minute), loc, true)
	if c := cellAt(cells, 11); c.Status != StatusWait {
		t.Errorf("12:04 주입 hour 11 status = %s, want wait", c.Status)
	}
}

// 과거 날짜 주입: 모든 hourly 셀이 skip (생성 이벤트가 이미 지남).
func TestPlanCellsPastDate(t *testing.T) {
	loc := mustLoc(t)
	inject := time.Date(2026, 8, 14, 9, 0, 0, 0, loc)
	cells := planCells(previewFor("2026-08-12", 5, 23), inject, loc, true)
	for _, c := range cells {
		if c.Status != StatusSkip {
			t.Errorf("과거 날짜 hour %d status = %s, want skip", c.Hour, c.Status)
		}
	}
}

// 어제 날짜의 23시: 오늘 00:05에 재생성됐지만 주입(09:00)이 그 뒤라 skip.
func TestPlanCellsYesterdayHour23(t *testing.T) {
	loc := mustLoc(t)
	inject := time.Date(2026, 8, 14, 9, 0, 0, 0, loc)
	cells := planCells(previewFor("2026-08-13", 23), inject, loc, true)
	if c := cellAt(cells, 23); c.Status != StatusSkip {
		t.Errorf("어제 23시 status = %s, want skip", c.Status)
	}
}

// 자정 직전(23:04) 주입: hour 21(22:05 생성)은 이미 놓쳤고, hour 23(익일 00:05)만 유효.
func TestPlanCellsLateNightInject(t *testing.T) {
	loc := mustLoc(t)
	inject := time.Date(2026, 8, 14, 23, 4, 0, 0, loc)
	cells := planCells(previewFor("2026-08-14", 21, 23), inject, loc, true)

	if c := cellAt(cells, 21); c.Status != StatusSkip {
		t.Errorf("23:04 주입 hour 21 status = %s, want skip (22:05 생성이 지남)", c.Status)
	}
	c23 := cellAt(cells, 23)
	if c23.Status != StatusWait {
		t.Fatalf("hour 23 status = %s, want wait (익일 00:05 생성)", c23.Status)
	}
	want := time.Date(2026, 8, 15, 0, 5, 0, 0, loc).Add(verify.HourlyGenMargin)
	if !c23.dueAt.Equal(want) {
		t.Errorf("hour 23 due = %v, want %v", c23.dueAt, want)
	}

	// 익일 00:06 주입이면 hour 23도 놓쳐 skip
	cells = planCells(previewFor("2026-08-14", 23), time.Date(2026, 8, 15, 0, 6, 0, 0, loc), loc, true)
	if c := cellAt(cells, 23); c.Status != StatusSkip {
		t.Errorf("익일 00:06 주입 hour 23 status = %s, want skip", c.Status)
	}
}

// 일별 셀도 생성 이벤트(익일 00:05)가 지나면 skip.
func TestPlanCellsDailySkip(t *testing.T) {
	loc := mustLoc(t)
	// 08-13치를 08-14 09:00에 주입 → 일별 생성(08-14 00:05)이 이미 지남
	cells := planCells(previewFor("2026-08-13", 5), time.Date(2026, 8, 14, 9, 0, 0, 0, loc), loc, false)
	d := cellAt(cells, -1)
	if d == nil || d.Status != StatusSkip {
		t.Fatalf("daily cell = %+v, want skip", d)
	}
	// 당일치를 당일 09:00에 주입하면 일별은 유효
	cells = planCells(previewFor("2026-08-14", 15), time.Date(2026, 8, 14, 9, 0, 0, 0, loc), loc, false)
	if d := cellAt(cells, -1); d.Status != StatusWait {
		t.Errorf("당일 daily status = %s, want wait", d.Status)
	}
}

func TestApplyVerifyRetryThenPassAndFail(t *testing.T) {
	loc := mustLoc(t)
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, loc)
	opt := Options{RetryDelay: 10 * time.Minute, MaxAttempts: 3}

	mk := func(hour int) *Cell {
		c := &Cell{CP: 100, Date: "2026-08-14", Hour: hour, Status: StatusWait}
		c.setDue(now.Add(-time.Minute))
		return c
	}
	res := &Result{Cells: []*Cell{mk(3), mk(4), mk(5)}}
	// hour 5는 아직 미도래
	res.Cells[2].setDue(now.Add(time.Hour))

	vres := &verify.Result{
		L2Daily:  verify.LayerResult{Ran: true},
		L2Hourly: verify.LayerResult{Ran: true, Mismatches: []verify.Mismatch{
			{Layer: "L2-hourly", CP: 100, Date: "2026-08-14", Hour: 4, Field: "row", Actual: "행 없음"},
		}},
	}
	applyVerify(res, vres, now, opt)

	if c := res.Cells[0]; c.Status != StatusPass {
		t.Errorf("hour 3 = %s, want pass", c.Status)
	}
	if c := res.Cells[1]; c.Status != StatusRecheck || c.Attempts != 1 {
		t.Errorf("hour 4 = %s attempts %d, want recheck/1", c.Status, c.Attempts)
	}
	if c := res.Cells[2]; c.Status != StatusWait || c.Attempts != 0 {
		t.Errorf("미도래 hour 5 = %s attempts %d, want wait/0", c.Status, c.Attempts)
	}

	// 재확인 2회 더 불일치 → fail 확정
	later := now.Add(11 * time.Minute)
	applyVerify(res, vres, later, opt)
	applyVerify(res, vres, later.Add(11*time.Minute), opt)
	if c := res.Cells[1]; c.Status != StatusFail || c.Attempts != 3 {
		t.Errorf("hour 4 = %s attempts %d, want fail/3", c.Status, c.Attempts)
	}
}

func TestApplyVerifyWindowViolationAndDaily(t *testing.T) {
	loc := mustLoc(t)
	now := time.Date(2026, 8, 15, 0, 30, 0, 0, loc)
	opt := Options{RetryDelay: 10 * time.Minute, MaxAttempts: 3}

	daily := &Cell{CP: 100, Date: "2026-08-14", Hour: -1, Status: StatusWait}
	daily.setDue(now.Add(-time.Minute))
	res := &Result{Cells: []*Cell{daily}}

	vres := &verify.Result{
		L2Daily: verify.LayerResult{Ran: true, Mismatches: []verify.Mismatch{
			{Layer: "L2-daily", CP: 100, Date: "2026-08-14", Hour: -1, Field: "daily_avg"},
		}},
		L2Hourly: verify.LayerResult{Ran: true, Mismatches: []verify.Mismatch{
			{Layer: "L2-hourly", CP: 100, Date: "2026-08-14", Hour: 3, Field: "row",
				Note: "창 밖 잔존 행 — 00:05 truncate 미동작 또는 시계 이상 의심"},
		}},
	}
	applyVerify(res, vres, now, opt)
	applyVerify(res, vres, now.Add(11*time.Minute), opt)
	applyVerify(res, vres, now.Add(22*time.Minute), opt)

	if daily.Status != StatusFail {
		t.Errorf("daily = %s, want fail", daily.Status)
	}
	if len(res.WindowViolations) != 1 {
		t.Fatalf("violations = %d, want 1 (중복 제거 포함)", len(res.WindowViolations))
	}
}

func TestNextDue(t *testing.T) {
	loc := mustLoc(t)
	a := &Cell{Status: StatusPass}
	b := &Cell{Status: StatusWait}
	b.setDue(time.Date(2026, 8, 14, 10, 0, 0, 0, loc))
	c := &Cell{Status: StatusRecheck}
	c.setDue(time.Date(2026, 8, 14, 9, 0, 0, 0, loc))
	got, ok := nextDue([]*Cell{a, b, c})
	if !ok || !got.Equal(c.dueAt) {
		t.Errorf("nextDue = %v %v, want %v", got, ok, c.dueAt)
	}
	if _, ok := nextDue([]*Cell{a}); ok {
		t.Error("완료 셀만 있으면 false여야 함")
	}
}
