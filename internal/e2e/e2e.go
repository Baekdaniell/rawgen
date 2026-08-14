// Package e2e는 "온종일 E2E"(계획서 §7)를 도구 안에서 한 번에 수행하는 러너다.
// 지금까지 PowerShell로 generate → (시간대별 대기) → verify → report를 사람이
// 반복 실행하던 것을 대체한다:
//
//	주입(generate+readback) → 즉시 L1 게이트 → 시간대별 마일스톤 대기
//	→ 시점 도래분만 verify 반복 → 셀(cp×날짜×시간) 단위 판정 누적 → 최종 리포트
//
// hourly는 보존 창(당일+전일 23시) 때문에 "언제 검증하느냐"가 결과를 좌우한다.
// 러너는 각 셀의 검증 가능 시점(생성 (H+1):05+여유, 주입 이후 첫 수확 이전이면
// 그 수확 시점)을 계산해 그때만 판정하고, 아직 시점이 안 된 셀의 '행 없음'은
// 실패로 세지 않는다. 불일치는 writer 부분 노출(2~4분 소요 실측)을 감안해
// 재확인 후 확정한다.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"oqt325/internal/executor"
	"oqt325/internal/model"
	"oqt325/internal/planner"
	"oqt325/internal/profile"
	"oqt325/internal/report"
	"oqt325/internal/verify"
)

// 일별 통계는 익일 00:05 생성 시작 + writer 소요 여유.
const dailyGenMargin = 20 * time.Minute

// verify 1회 실행 상한(app.go RunVerify와 동일 기준).
const defaultVerifyTimeout = 10 * time.Minute

// 셀 상태.
const (
	StatusWait    = "wait"    // 검증 시점 미도래
	StatusRecheck = "recheck" // 불일치 검출 — 재확인 대기
	StatusPass    = "pass"
	StatusFail    = "fail"
	StatusSkip    = "skip" // 주입 시점에 이미 보존 창이 지나 검증 불가
)

// Cell은 판정 단위다. Hour == -1이면 일별(daily_stats) 셀.
// DueRFC는 상태 영속화(재개)용 — dueAt의 유일한 직렬화 표현이다.
type Cell struct {
	CP         int64             `json:"cp"`
	Date       string            `json:"date"`
	Hour       int               `json:"hour"`
	Due        string            `json:"due"` // 표시용 (다음 판정 시각)
	DueRFC     string            `json:"dueRFC,omitempty"`
	Status     string            `json:"status"`
	CheckedAt  string            `json:"checkedAt,omitempty"`
	Attempts   int               `json:"attempts"`
	Note       string            `json:"note,omitempty"`
	Mismatches []verify.Mismatch `json:"mismatches,omitempty"`

	dueAt time.Time
}

type Progress struct {
	Phase     string  `json:"phase"` // generate | l1 | wait | verify | cells | done
	Message   string  `json:"message,omitempty"`
	NextAt    string  `json:"nextAt,omitempty"`
	NextAtRFC string  `json:"nextAtRFC,omitempty"` // 프런트 카운트다운용
	EstEndRFC string  `json:"estEndRFC,omitempty"` // pending 셀 중 가장 늦은 due
	Done      int     `json:"done,omitempty"`
	Total     int     `json:"total,omitempty"`
	Cells     []*Cell `json:"cells,omitempty"`
}

type Options struct {
	SkipGenerate  bool          // 이미 주입된 시나리오를 이어서 검증만
	KeepGoing     bool          // L1 불합격·readback 실패에도 계속 진행
	RetryDelay    time.Duration // 불일치 재확인 간격 (0 = 10분)
	MaxAttempts   int           // 셀당 판정 시도 상한 (0 = 3)
	VerifyTimeout time.Duration // verify 1회 상한 (0 = 10분)
	SkipDaily     bool          // 일별 셀 제외 (자정 전 종료하고 싶을 때)

	// Resume은 이전 실행의 저장 상태다. 시나리오가 동일하면 셀 판정 이력을
	// 복원해 pending 셀만 이어서 판정한다(주입·완료 셀 재실행 없음).
	Resume *Result `json:"-"`
	// OnState는 상태가 바뀔 때마다(계획 직후·판정 반영 후) 호출된다.
	// 앱 계층이 파일로 영속화해 크래시 후 재개에 쓴다. 실패해도 실행엔 영향 없음.
	OnState func(*Result) `json:"-"`
}

type Result struct {
	Scenario         model.Scenario      `json:"scenario"`
	Profile          string              `json:"profile"`
	StartedAt        string              `json:"startedAt"`
	FinishedAt       string              `json:"finishedAt"`
	Run              *executor.RunResult `json:"run,omitempty"`
	L1Pass           bool                `json:"l1Pass"`
	L1               *verify.Result      `json:"l1,omitempty"`
	FinalVerify      *verify.Result      `json:"finalVerify,omitempty"`
	Cells            []*Cell             `json:"cells"`
	WindowViolations []verify.Mismatch   `json:"windowViolations,omitempty"`
	VerifyRuns       int                 `json:"verifyRuns"`
	KeepGoing        bool                `json:"keepGoing,omitempty"`
	Pass             bool                `json:"pass"`
	Aborted          string              `json:"aborted,omitempty"`
	Resumed          bool                `json:"resumed,omitempty"`
}

// PlanSummary는 실행 전 사전 점검(preflight)용 계획 요약이다.
type PlanSummary struct {
	TotalCells int    `json:"totalCells"`
	SkipCells  int    `json:"skipCells"`
	FirstDue   string `json:"firstDue"` // RFC3339, skip 제외 최소 due
	LastDue    string `json:"lastDue"`  // RFC3339, skip 제외 최대 due (예상 종료)
}

// PlanPreview는 DB 접근 없이 시나리오만으로 셀 계획을 요약한다.
func PlanPreview(s model.Scenario, injectAt time.Time, skipDaily bool) (*PlanSummary, error) {
	loc, err := s.Location()
	if err != nil {
		return nil, err
	}
	pv, err := planner.Build(s, injectAt)
	if err != nil {
		return nil, err
	}
	cells := planCells(pv, injectAt, loc, skipDaily)
	sum := &PlanSummary{TotalCells: len(cells)}
	var first, last time.Time
	for _, c := range cells {
		if c.Status == StatusSkip {
			sum.SkipCells++
			continue
		}
		if first.IsZero() || c.dueAt.Before(first) {
			first = c.dueAt
		}
		if c.dueAt.After(last) {
			last = c.dueAt
		}
	}
	if !first.IsZero() {
		sum.FirstDue = first.Format(time.RFC3339)
		sum.LastDue = last.Format(time.RFC3339)
	}
	return sum, nil
}

// nextHarvest는 t 이후 처음 오는 매시 :05(제품 hourly job 실행 시각)다.
func nextHarvest(t time.Time) time.Time {
	h := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 5, 0, 0, t.Location())
	if !t.Before(h) {
		h = h.Add(time.Hour)
	}
	return h
}

// planCells는 (cp×날짜×시간 + 일별) 셀과 각각의 판정 시점을 만든다.
// injectAt = 주입 완료 시각. 주입 전에 지난 시간대는 다음 :05 백필이 수확하므로
// firstHarvest(+여유) 이전에는 판정하지 않는다. 주입 시점에 이미 보존 창이 끝난
// 셀(익일 00:05 truncate 이후)은 skip으로 표시한다 — 과거 날짜 시나리오가 여기 해당.
func planCells(pv *model.Preview, injectAt time.Time, loc *time.Location, skipDaily bool) []*Cell {
	firstHarvest := nextHarvest(injectAt).Add(verify.HourlyGenMargin)
	var cells []*Cell
	for _, de := range pv.Days {
		day, err := time.ParseInLocation("2006-01-02", de.Date, loc)
		if err != nil {
			continue
		}
		for _, he := range de.Hours {
			c := &Cell{CP: de.CheckpointID, Date: de.Date, Hour: he.Hour, Status: StatusWait}
			due := verify.HourlyDueAt(de.Date, he.Hour, loc)
			if due.Before(firstHarvest) {
				due = firstHarvest
			}
			// (D,H)가 hourly 테이블에 반영되는 이벤트는 정규 생성 (H+1):05 하나뿐이다.
			// 백필(generateForMissingHourlyStats)은 그 (date,hour)의 hourly 행이 통째로
			// 0일 때만 재생성하는데, 운영 중인 환경은 다른 checkpoint의 행이 이미 있어
			// 0이 되지 않는다(koscom 실측: 시간당 65만 행). 따라서 주입이 (H+1):05를
			// 넘겼다면 그 셀은 영원히 반영되지 않는다 — 백필에 기대면 안 된다.
			// hour 23도 (23+1):05 = 익일 00:05(truncate 직후 재생성)라 같은 식으로 계산된다.
			lastEvent := day.Add(time.Duration(he.Hour+1)*time.Hour + 5*time.Minute)
			if !injectAt.Before(lastEvent) {
				c.Status = StatusSkip
				c.Note = "주입 시점에 이 시간대의 통계 생성((H+1):05)이 이미 지나 대조 불가 — 백필은 해당 시간대 행이 통째로 0일 때만 재생성한다"
			}
			c.setDue(due)
			cells = append(cells, c)
		}
		if !skipDaily {
			d := &Cell{CP: de.CheckpointID, Date: de.Date, Hour: -1, Status: StatusWait}
			// 일별 통계도 생성 이벤트는 익일 00:05 하나뿐이다. 누락일 재생성은 그 날짜의
			// MariaDB 통계 행이 없을 때만 도는데, 운영 환경은 이미 행이 있어 돌지 않는다.
			dailyEvent := day.AddDate(0, 0, 1).Add(5 * time.Minute)
			due := dailyEvent.Add(dailyGenMargin)
			if due.Before(firstHarvest) {
				due = firstHarvest
			}
			if !injectAt.Before(dailyEvent) {
				d.Status = StatusSkip
				d.Note = "주입 시점에 일별 통계 생성(익일 00:05)이 이미 지나 대조 불가"
			}
			d.setDue(due)
			cells = append(cells, d)
		}
	}
	sort.SliceStable(cells, func(i, j int) bool { return cells[i].dueAt.Before(cells[j].dueAt) })
	return cells
}

func (c *Cell) setDue(t time.Time) {
	c.dueAt = t
	c.Due = t.Format("01-02 15:04")
	c.DueRFC = t.Format(time.RFC3339)
}

// restoreCells는 저장 상태에서 불러온 셀의 dueAt을 DueRFC에서 복원한다.
func restoreCells(cells []*Cell) []*Cell {
	for _, c := range cells {
		if t, err := time.Parse(time.RFC3339, c.DueRFC); err == nil {
			c.dueAt = t
		} else if c.pending() {
			// due를 잃은 pending 셀은 즉시 판정 대상으로
			c.setDue(time.Now())
		}
	}
	return cells
}

// sameScenario는 재개 시 시나리오 동일성을 판정한다(JSON 표현 기준).
func sameScenario(a, b model.Scenario) bool {
	ja, ea := json.Marshal(a)
	jb, eb := json.Marshal(b)
	return ea == nil && eb == nil && string(ja) == string(jb)
}

func (c *Cell) pending() bool {
	return c.Status == StatusWait || c.Status == StatusRecheck
}

// Run은 E2E 전체를 실행한다. res는 planner 성공 이후엔 항상 non-nil이며,
// 중단 시 res.Aborted에 사유가 남는다(부분 리포트 생성 가능).
func Run(ctx context.Context, p profile.Profile, s model.Scenario, opt Options, emit func(Progress)) (*Result, error) {
	if opt.RetryDelay <= 0 {
		opt.RetryDelay = 10 * time.Minute
	}
	if opt.MaxAttempts <= 0 {
		opt.MaxAttempts = 3
	}
	if opt.VerifyTimeout <= 0 {
		opt.VerifyTimeout = defaultVerifyTimeout
	}
	if emit == nil {
		emit = func(Progress) {}
	}

	loc, err := s.Location()
	if err != nil {
		return nil, err
	}
	pv, err := planner.Build(s, time.Now())
	if err != nil {
		return nil, err
	}

	res := &Result{Scenario: s, Profile: p.Name, StartedAt: time.Now().Format(time.RFC3339), KeepGoing: opt.KeepGoing}
	saveState := func() {
		if opt.OnState != nil {
			opt.OnState(res)
		}
	}
	abort := func(reason string) (*Result, error) {
		res.Aborted = reason
		res.FinishedAt = time.Now().Format(time.RFC3339)
		saveState()
		emit(Progress{Phase: "done", Message: "중단: " + reason})
		return res, nil
	}

	if opt.Resume != nil && !sameScenario(opt.Resume.Scenario, s) {
		return nil, fmt.Errorf("재개 상태의 시나리오와 현재 시나리오가 다릅니다 — 저장된 시나리오를 불러온 뒤 다시 시도하세요")
	}

	// ---- 1. 주입 ----
	if !opt.SkipGenerate {
		emit(Progress{Phase: "generate", Message: "주입 시작 (batch INSERT + readback)"})
		run, err := executor.Run(ctx, p, s, false, func(pr executor.Progress) {
			if pr.Message != "" {
				emit(Progress{Phase: "generate", Message: fmt.Sprintf("[%s] %s", pr.Phase, pr.Message)})
			} else {
				emit(Progress{Phase: "generate", Done: int(pr.Done), Total: int(pr.Total),
					Message: fmt.Sprintf("[insert] cp %d %s: %d/%d", pr.CP, pr.Date, pr.Done, pr.Total)})
			}
		})
		res.Run = run
		if err != nil {
			return abort("주입 실패: " + err.Error())
		}
		if run.Error != "" {
			return abort("주입 실패: " + run.Error)
		}
		if run.Canceled {
			return abort("주입 취소됨")
		}
		for _, d := range run.Days {
			if !d.ReadbackOK && !opt.KeepGoing {
				return abort(fmt.Sprintf("readback 실패(cp %d %s): %s — 주입이 확인되지 않아 E2E를 중단합니다", d.CheckpointID, d.Date, d.ReadbackNote))
			}
		}
	}

	// ---- 2. L1 게이트 (주입 직후, 재생성 불필요) ----
	emit(Progress{Phase: "l1", Message: "L1 검증 (expected ↔ checkvalue ↔ MV)"})
	var l1 *verify.Result
	for attempt := 1; ; attempt++ {
		l1, err = verifyOnce(ctx, p, s, true, opt.VerifyTimeout)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return abort("취소됨 (L1 검증 중)")
		}
		if attempt >= 3 {
			return abort("L1 검증 실행 실패(3회): " + err.Error())
		}
		emit(Progress{Phase: "l1", Message: fmt.Sprintf("L1 재시도 %d/3: %s", attempt, err.Error())})
		if err := sleepUntil(ctx, time.Now().Add(2*time.Minute)); err != nil {
			return abort("취소됨 (L1 재시도 대기 중)")
		}
	}
	res.L1 = l1
	res.L1Pass = l1.L1Raw.Pass && l1.L1MV.Pass
	if !res.L1Pass {
		emit(Progress{Phase: "l1", Message: "L1 불합격 — 주입 결과가 기대값과 다릅니다"})
		if !opt.KeepGoing {
			return abort("L1 불합격 — 주입 자체가 기대와 다르므로 L2 대기는 무의미합니다 ('불합격에도 계속' 옵션으로 강행 가능)")
		}
	} else {
		emit(Progress{Phase: "l1", Message: "L1 통과"})
	}

	// ---- 3. 마일스톤 계획 ----
	if opt.Resume != nil {
		// 저장 상태 재개: 완료 셀은 그대로, pending 셀만 이어서 판정
		res.Cells = restoreCells(opt.Resume.Cells)
		res.WindowViolations = opt.Resume.WindowViolations
		res.VerifyRuns = opt.Resume.VerifyRuns
		res.Resumed = true
		if res.Run == nil {
			res.Run = opt.Resume.Run
		}
		done, total := cellCounts(res.Cells)
		emit(Progress{Phase: "l1", Message: fmt.Sprintf("저장 상태 재개 — 셀 %d/%d 판정 완료분 복원", done, total)})
	} else {
		// 주입 생략 시엔 실제 주입 시각을 알 수 없으므로 시나리오 시작일 00시로
		// 간주한다 — 이미 수확된 시간대를 다음 :05까지 다시 기다리지 않고 즉시 판정.
		injectAt := time.Now()
		if opt.SkipGenerate {
			if d, err := time.ParseInLocation("2006-01-02", s.StartDate, loc); err == nil {
				injectAt = d
			}
		}
		res.Cells = planCells(pv, injectAt, loc, opt.SkipDaily)
	}
	saveState()
	emitCells(emit, res.Cells)

	// ---- 4. 마일스톤 루프 ----
	errStreak := 0
	for {
		next, ok := nextDue(res.Cells)
		if !ok {
			break
		}
		if wait := time.Until(next); wait > 0 {
			emit(Progress{Phase: "wait", NextAt: next.Format("01-02 15:04"),
				NextAtRFC: next.Format(time.RFC3339), EstEndRFC: estEnd(res.Cells),
				Message: fmt.Sprintf("다음 판정 %s (%s 후)", next.Format("15:04"), wait.Round(time.Minute))})
			if err := sleepUntil(ctx, next); err != nil {
				return abort("취소됨 (마일스톤 대기 중)")
			}
		}

		emit(Progress{Phase: "verify", Message: "verify 실행 (L1+L2)"})
		vres, err := verifyOnce(ctx, p, s, false, opt.VerifyTimeout)
		res.VerifyRuns++
		if err != nil {
			if ctx.Err() != nil {
				return abort("취소됨 (verify 실행 중)")
			}
			errStreak++
			// 복제 지연 가드 차단 등은 일시 상태일 수 있어 간격을 두고 재시도한다
			emit(Progress{Phase: "verify", Message: fmt.Sprintf("verify 실패(연속 %d회): %s — %s 후 재시도", errStreak, err.Error(), opt.RetryDelay)})
			if errStreak >= 5 {
				return abort("verify 연속 실패 5회: " + err.Error())
			}
			if err := sleepUntil(ctx, time.Now().Add(opt.RetryDelay)); err != nil {
				return abort("취소됨 (verify 재시도 대기 중)")
			}
			continue
		}
		errStreak = 0
		res.FinalVerify = vres
		applyVerify(res, vres, time.Now(), opt)
		saveState()
		emitCells(emit, res.Cells)
	}

	// ---- 5. 종합 ----
	res.Pass = res.L1Pass && len(res.WindowViolations) == 0
	for _, c := range res.Cells {
		if c.Status == StatusFail {
			res.Pass = false
		}
	}
	res.FinishedAt = time.Now().Format(time.RFC3339)
	saveState()
	done, total := cellCounts(res.Cells)
	emit(Progress{Phase: "done", Done: done, Total: total,
		Message: fmt.Sprintf("E2E 완료: %s (verify %d회)", passText(res.Pass), res.VerifyRuns)})
	return res, nil
}

// estEnd는 pending 셀 중 가장 늦은 due(예상 종료 시각)를 RFC3339로 돌려준다.
func estEnd(cells []*Cell) string {
	var last time.Time
	for _, c := range cells {
		if c.pending() && c.dueAt.After(last) {
			last = c.dueAt
		}
	}
	if last.IsZero() {
		return ""
	}
	return last.Format(time.RFC3339)
}

func verifyOnce(ctx context.Context, p profile.Profile, s model.Scenario, skipL2 bool, timeout time.Duration) (*verify.Result, error) {
	vctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// 셀 판정이 mismatch 목록에 의존하므로 표본 상한을 사실상 해제한다
	return verify.Run(vctx, p, s, verify.Options{SkipL2: skipL2, MaxSamples: 1_000_000})
}

func nextDue(cells []*Cell) (time.Time, bool) {
	var t time.Time
	found := false
	for _, c := range cells {
		if !c.pending() {
			continue
		}
		if !found || c.dueAt.Before(t) {
			t = c.dueAt
			found = true
		}
	}
	return t, found
}

// applyVerify는 판정 시점이 도래한 셀에 이번 verify 결과를 반영한다.
func applyVerify(res *Result, vres *verify.Result, now time.Time, opt Options) {
	hourly := map[string][]verify.Mismatch{}
	daily := map[string][]verify.Mismatch{}
	for _, m := range vres.L2Hourly.Mismatches {
		if strings.Contains(m.Note, "창 밖") {
			addViolation(res, m)
			continue
		}
		k := fmt.Sprintf("%d|%s|%d", m.CP, m.Date, m.Hour)
		hourly[k] = append(hourly[k], m)
	}
	for _, m := range vres.L2Daily.Mismatches {
		k := fmt.Sprintf("%d|%s", m.CP, m.Date)
		daily[k] = append(daily[k], m)
	}

	for _, c := range res.Cells {
		if !c.pending() || c.dueAt.After(now) {
			continue
		}
		var ms []verify.Mismatch
		if c.Hour < 0 {
			ms = daily[fmt.Sprintf("%d|%s", c.CP, c.Date)]
			if !vres.L2Daily.Ran {
				continue
			}
		} else {
			ms = hourly[fmt.Sprintf("%d|%s|%d", c.CP, c.Date, c.Hour)]
			if !vres.L2Hourly.Ran {
				continue
			}
		}
		c.Attempts++
		if len(ms) == 0 {
			c.Status = StatusPass
			c.CheckedAt = now.Format("01-02 15:04")
			c.Mismatches = nil
			if c.Attempts > 1 {
				c.Note = fmt.Sprintf("재확인 %d회 후 통과 (writer 지연/부분 노출 가능성)", c.Attempts-1)
			}
			continue
		}
		c.Mismatches = ms
		if c.Attempts >= opt.MaxAttempts {
			c.Status = StatusFail
			c.CheckedAt = now.Format("01-02 15:04")
			c.Note = fmt.Sprintf("불일치 %d건 (판정 %d회 모두 불일치)", len(ms), c.Attempts)
		} else {
			c.Status = StatusRecheck
			c.setDue(now.Add(opt.RetryDelay))
			c.Note = fmt.Sprintf("불일치 %d건 — %s 재확인 예정 (%d/%d)", len(ms), c.Due, c.Attempts, opt.MaxAttempts)
		}
	}
}

func addViolation(res *Result, m verify.Mismatch) {
	for _, v := range res.WindowViolations {
		if v.CP == m.CP && v.Date == m.Date && v.Hour == m.Hour && v.Field == m.Field {
			return
		}
	}
	res.WindowViolations = append(res.WindowViolations, m)
}

func cellCounts(cells []*Cell) (done, total int) {
	for _, c := range cells {
		total++
		if !c.pending() {
			done++
		}
	}
	return
}

func emitCells(emit func(Progress), cells []*Cell) {
	done, total := cellCounts(cells)
	emit(Progress{Phase: "cells", Cells: cells, Done: done, Total: total})
}

func sleepUntil(ctx context.Context, t time.Time) error {
	d := time.Until(t)
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func passText(pass bool) string {
	if pass {
		return "PASS"
	}
	return "FAIL"
}

// Markdown은 E2E 결과 리포트를 만든다. 셀 타임라인 + 최종 verify 리포트를 잇는다.
func Markdown(r *Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# E2E 결과 — %s\n\n", r.Scenario.Name)
	fmt.Fprintf(&b, "- 프로파일: %s / 판정: **%s**\n", r.Profile, passText(r.Pass))
	fmt.Fprintf(&b, "- 시작 %s → 종료 %s (verify %d회)\n", r.StartedAt, r.FinishedAt, r.VerifyRuns)
	if r.Run != nil {
		fmt.Fprintf(&b, "- 주입: run_id=%s rows=%d\n", r.Run.RunID, r.Run.TotalRows)
	} else {
		b.WriteString("- 주입: 생략(이미 주입된 시나리오 검증)\n")
	}
	fmt.Fprintf(&b, "- L1(주입 직후): %s\n", passText(r.L1Pass))
	if r.KeepGoing {
		b.WriteString("- 주의: keep-going 모드로 실행됨 — L1/readback 불합격을 무시하고 진행한 진단용 결과이므로 합격 판정 근거로 쓰지 말 것\n")
	}
	if r.Resumed {
		b.WriteString("- 이 결과는 저장 상태에서 재개된 실행입니다 (완료 셀 판정은 이전 실행분)\n")
	}
	if r.Aborted != "" {
		fmt.Fprintf(&b, "- ⚠️ 중단: %s\n", r.Aborted)
	}

	var pass, fail, skip, pending int
	for _, c := range r.Cells {
		switch c.Status {
		case StatusPass:
			pass++
		case StatusFail:
			fail++
		case StatusSkip:
			skip++
		default:
			pending++
		}
	}
	fmt.Fprintf(&b, "- 셀: pass %d / fail %d / skip %d / 미도래 %d (총 %d)\n\n", pass, fail, skip, pending, len(r.Cells))

	b.WriteString("## 셀 타임라인 (cp × 날짜 × 시간, hour=-1은 일별)\n\n")
	b.WriteString("| cp | date | hour | 판정 시점 | 상태 | 시도 | 비고 |\n|---|---|---|---|---|---|---|\n")
	for _, c := range r.Cells {
		hour := fmt.Sprint(c.Hour)
		if c.Hour < 0 {
			hour = "일별"
		}
		at := c.CheckedAt
		if at == "" {
			at = c.Due
		}
		fmt.Fprintf(&b, "| %d | %s | %s | %s | %s | %d | %s |\n", c.CP, c.Date, hour, at, c.Status, c.Attempts, c.Note)
	}
	b.WriteString("\n")

	var failCells []*Cell
	for _, c := range r.Cells {
		if c.Status == StatusFail {
			failCells = append(failCells, c)
		}
	}
	if len(failCells) > 0 {
		b.WriteString("## 불일치 상세\n\n| cp | date | hour | field | expected | actual | note |\n|---|---|---|---|---|---|---|\n")
		for _, c := range failCells {
			for _, m := range c.Mismatches {
				fmt.Fprintf(&b, "| %d | %s | %d | %s | %s | %s | %s |\n", m.CP, m.Date, m.Hour, m.Field, m.Expected, m.Actual, m.Note)
			}
		}
		b.WriteString("\n")
	}
	if len(r.WindowViolations) > 0 {
		b.WriteString("## ⚠️ 보존 창 밖 잔존 행 (truncate 미동작 의심)\n\n| cp | date | hour | note |\n|---|---|---|---|\n")
		for _, m := range r.WindowViolations {
			fmt.Fprintf(&b, "| %d | %s | %d | %s |\n", m.CP, m.Date, m.Hour, m.Note)
		}
		b.WriteString("\n")
	}

	if r.FinalVerify != nil {
		b.WriteString("---\n\n## 최종 verify 상세\n\n")
		b.WriteString(report.Markdown(report.Bundle{Run: r.Run, Verify: r.FinalVerify}))
	}
	return b.String()
}
