package e2e

import (
	"testing"
	"time"

	"rawgen/internal/verify"
)

func gopt() Options { return Options{RetryDelay: 10 * time.Minute, MaxAttempts: 3} }

// 층 조회가 실패한 회차의 "불일치 0"은 통과가 아니라 미검증이다.
// 이 회차에 셀을 확정해버리면 제품 hourly가 전부 틀려 있어도 전 셀이 PASS가 된다.
func TestApplyVerifyHourlyErrorDoesNotConfirmCells(t *testing.T) {
	loc := mustLoc(t)
	now := time.Date(2026, 8, 14, 12, 15, 0, 0, loc)
	c := &Cell{CP: 100, Date: "2026-08-14", Hour: 11, Status: StatusWait}
	c.setDue(time.Date(2026, 8, 14, 12, 15, 0, 0, loc))
	res := &Result{Cells: []*Cell{c}}

	vres := &verify.Result{L2Hourly: verify.LayerResult{Ran: true, Errored: true, Err: "SHOW COLUMNS 실패"}}
	applyVerify(res, vres, now, loc, gopt())

	if c.Status != StatusWait {
		t.Errorf("조회 실패 회차 status = %s, want wait (확정하면 안 됨)", c.Status)
	}
	if c.Attempts != 0 {
		t.Errorf("attempts = %d, want 0 (대조를 하지 않았으므로 시도로 세지 않는다)", c.Attempts)
	}

	// 같은 셀이 정상 회차에는 통과로 확정된다
	applyVerify(res, &verify.Result{L2Hourly: verify.LayerResult{Ran: true}}, now, loc, gopt())
	if c.Status != StatusPass {
		t.Errorf("정상 회차 status = %s, want pass", c.Status)
	}
}

// daily도 동일하게 조회 실패 회차에는 확정하지 않는다.
func TestApplyVerifyDailyErrorDoesNotConfirmCells(t *testing.T) {
	loc := mustLoc(t)
	now := time.Date(2026, 8, 15, 0, 30, 0, 0, loc)
	c := &Cell{CP: 100, Date: "2026-08-14", Hour: -1, Status: StatusWait}
	c.setDue(time.Date(2026, 8, 15, 0, 25, 0, 0, loc))
	res := &Result{Cells: []*Cell{c}}

	applyVerify(res, &verify.Result{L2Daily: verify.LayerResult{Ran: true, Errored: true, Err: "조회 실패"}}, now, loc, gopt())
	if c.Status != StatusWait {
		t.Errorf("status = %s, want wait", c.Status)
	}
}

// 이미 불일치를 관측한 셀이 재확인 대기 중 보존 창을 넘으면 skip으로 세탁하지 않는다.
// skip으로 덮으면 검출된 제품 결함이 리포트에서 사라진다.
func TestWindowExpiryOnObservedMismatchIsFailNotSkip(t *testing.T) {
	loc := mustLoc(t)
	// 08-14 17시 셀을 다음날 09:00에 판정 → 창 밖
	now := time.Date(2026, 8, 15, 9, 0, 0, 0, loc)
	c := &Cell{CP: 100, Date: "2026-08-14", Hour: 17, Status: StatusRecheck, Attempts: 2,
		Mismatches: []verify.Mismatch{{Layer: "L2-hourly", CP: 100, Date: "2026-08-14", Hour: 17, Field: "avg"}}}
	c.setDue(time.Date(2026, 8, 15, 8, 0, 0, 0, loc))
	res := &Result{Cells: []*Cell{c}}

	applyVerify(res, &verify.Result{L2Hourly: verify.LayerResult{Ran: true}}, now, loc, gopt())
	if c.Status != StatusFail {
		t.Errorf("status = %s, want fail (관측된 불일치를 미검증으로 세탁하면 안 됨)", c.Status)
	}
	if len(c.Mismatches) == 0 {
		t.Error("불일치 상세가 지워졌다")
	}
}

// 한 번도 대조하지 못한 셀은 기존 규칙대로 skip이다(HANDOFF 규정 유지).
func TestWindowExpiryWithoutObservationStaysSkip(t *testing.T) {
	loc := mustLoc(t)
	now := time.Date(2026, 8, 15, 9, 0, 0, 0, loc)
	c := &Cell{CP: 100, Date: "2026-08-14", Hour: 17, Status: StatusWait}
	c.setDue(time.Date(2026, 8, 14, 18, 15, 0, 0, loc))
	res := &Result{Cells: []*Cell{c}}

	applyVerify(res, &verify.Result{L2Hourly: verify.LayerResult{Ran: true}}, now, loc, gopt())
	if c.Status != StatusSkip {
		t.Errorf("status = %s, want skip", c.Status)
	}
	if c.PlannedSkip {
		t.Error("실행 중 강등은 PlannedSkip이 아니어야 한다(최종 판정에서 감점 대상)")
	}
}

// 재확인 시각이 보존 창 만료를 넘으면 창 안으로 당긴다.
func TestRecheckDueClampedIntoWindow(t *testing.T) {
	loc := mustLoc(t)
	// 08-14 23시 셀의 창 만료는 08-16 00:05. 08-15 23:58에 불일치를 보면
	// +10분 재확인은 창을 넘는다 → 만료 2분 전으로 당겨야 한다.
	now := time.Date(2026, 8, 15, 23, 58, 0, 0, loc)
	c := &Cell{CP: 100, Date: "2026-08-14", Hour: 23, Status: StatusWait}
	c.setDue(now.Add(-time.Minute))
	res := &Result{Cells: []*Cell{c}}
	ms := []verify.Mismatch{{Layer: "L2-hourly", CP: 100, Date: "2026-08-14", Hour: 23, Field: "avg"}}
	vres := &verify.Result{L2Hourly: verify.LayerResult{Ran: true, Mismatches: ms}}

	applyVerify(res, vres, now, loc, gopt())
	if c.Status != StatusRecheck {
		t.Fatalf("status = %s, want recheck", c.Status)
	}
	end := verify.HourlyWindowEnd("2026-08-14", 23, loc)
	if !c.dueAt.Before(end) {
		t.Errorf("재확인 due %v가 창 만료 %v 이후다", c.dueAt, end)
	}
}

// 판정된 셀이 하나도 없으면 PASS가 아니라 INCONCLUSIVE다.
func TestFinalizeNoJudgedCellIsInconclusive(t *testing.T) {
	res := &Result{L1Pass: true, Cells: []*Cell{}}
	finalize(res)
	if res.Pass {
		t.Error("셀 0개인데 PASS가 나왔다")
	}
	if !res.Inconclusive {
		t.Error("Inconclusive가 아니다")
	}
}

// 실행 중 강등된 skip이 남아 있으면 PASS 불가. 계획 단계 skip은 감점하지 않는다.
func TestFinalizeRuntimeSkipBlocksPass(t *testing.T) {
	runtimeSkip := &Result{L1Pass: true, Cells: []*Cell{
		{Status: StatusPass},
		{Status: StatusSkip}, // PlannedSkip=false → 실행 중 강등
	}}
	finalize(runtimeSkip)
	if runtimeSkip.Pass {
		t.Error("실행 중 강등 skip이 남았는데 PASS가 나왔다")
	}
	if !runtimeSkip.Inconclusive {
		t.Error("Inconclusive여야 한다")
	}

	planned := &Result{L1Pass: true, Cells: []*Cell{
		{Status: StatusPass},
		{Status: StatusSkip, PlannedSkip: true},
	}}
	finalize(planned)
	if !planned.Pass {
		t.Errorf("계획 단계 skip은 감점 대상이 아니다 (counts=%+v)", planned.Counts)
	}

	failed := &Result{L1Pass: true, Cells: []*Cell{{Status: StatusPass}, {Status: StatusFail}}}
	finalize(failed)
	if failed.Pass || failed.Inconclusive {
		t.Error("fail 셀이 있으면 FAIL이어야 한다")
	}
}
