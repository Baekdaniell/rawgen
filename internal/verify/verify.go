// Package verify는 두 층의 검증을 수행한다(계획서 §1.1).
//   - L1 (주입 직후, 재생성 불필요): expected ↔ checkvalue 재집계 ↔ daily_stats(CH) MV
//     → MV 집계 로직 검증. CH↔CH는 사실상 정확 일치, expected↔CH는 1e-9 상대오차.
//   - L2 (재생성 후): expected(=L1 통과 시 MV와 동치) ↔ MariaDB daily_stats / hourly
//     → 피봇·writer 로직 검증. hour 매핑(daily _H+1 / hourly H↔H), NULL 처리,
//       count 가중평균, max_time. MariaDB 경유는 전 항목 1e-6(텍스트 왕복 잡음).
package verify

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"oqt325/internal/chdb"
	"oqt325/internal/generator"
	"oqt325/internal/mariadb"
	"oqt325/internal/model"
	"oqt325/internal/planner"
	"oqt325/internal/profile"
)

const chTolerance = 1e-9 // expected(Go float64) ↔ CH 집계 순서 차이 흡수

// hourly 생성 여유: H시 통계는 (H+1):05에 생성 시작, writer가 전 checkpoint를
// 배치(1000행+100ms 슬립)로 쓰므로 완료까지 수 분 걸릴 수 있다(koscom 34만 cp 실측 수 분).
const hourlyGenMargin = 10 * time.Minute

// HourlyGenMargin은 E2E 러너가 마일스톤 계산에 쓰는 동일 여유값이다.
const HourlyGenMargin = hourlyGenMargin

// HourlyDueAt는 (date, hour) 행이 "존재할 것으로 기대되는 최초 시각"이다.
// H시 통계는 (H+1):05에 생성 시작하므로 여유(hourlyGenMargin)를 더해 돌려준다.
// E2E 러너가 마일스톤(검증 시점) 계산에 같이 쓴다.
func HourlyDueAt(date string, hour int, loc *time.Location) time.Time {
	day, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		return time.Time{}
	}
	return day.Add(time.Duration(hour+1)*time.Hour + 5*time.Minute + hourlyGenMargin)
}

// HourlyInWindow는 (date, hour) 행이 지금 보존 창 안이라 대조가 유의미한지 알려준다.
// E2E 러너가 "대조 자체가 불가능한 셀"을 통과로 오판하지 않도록 쓰는 공개 창구다.
func HourlyInWindow(date string, hour int, now time.Time, loc *time.Location) bool {
	return hourlyInWindow(date, hour, now, loc)
}

// HourlyWindowEnd는 (date, hour) 행이 보존 창에서 사라지는 시각이다.
// 당일 행은 익일 00:05 truncate 때, 전일 23시 행은 그 다음 00:05까지 잔존한다.
// E2E 러너가 "재확인을 창 만료 전에 끝낼 수 있는가"를 판단하는 데 쓴다.
func HourlyWindowEnd(date string, hour int, loc *time.Location) time.Time {
	day, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		return time.Time{}
	}
	// 익일 00:05 truncate. hour 23은 그 직후 재생성되어 하루 더 남는다.
	end := day.AddDate(0, 0, 1).Add(5 * time.Minute)
	if hour == 23 {
		end = end.AddDate(0, 0, 1)
	}
	return end
}

// hourlyInWindow는 제품의 hourly 보존 창 기준으로 (date, hour) 행이
// "지금 존재해야 정상"인지 판정한다.
//   - H시 행은 (H+1):05 + 여유 이후에야 존재한다 (매시 :05 생성)
//   - 익일 00:05 truncate로 전날 00~22시는 소멸, 23시 행만 truncate 직후
//     재생성되어 그 날 하루 잔존한다 (당일 + 전일 23시만 잔존)
func hourlyInWindow(date string, hour int, now time.Time, loc *time.Location) bool {
	day, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		return false
	}
	// 아직 생성 시각이 안 됐으면 창 밖
	if now.Before(HourlyDueAt(date, hour, loc)) {
		return false
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	if day.Equal(today) {
		return true
	}
	if day.AddDate(0, 0, 1).Equal(today) && hour == 23 {
		return true
	}
	return false
}

type Mismatch struct {
	Layer    string  `json:"layer"` // L1-raw | L1-mv | L2-daily | L2-hourly
	CP       int64   `json:"cp"`
	Date     string  `json:"date"`
	Hour     int     `json:"hour"` // -1 = 일별 대푯값
	Field    string  `json:"field"`
	Expected string  `json:"expected"`
	Actual   string  `json:"actual"`
	Diff     float64 `json:"diff"`
	Note     string  `json:"note,omitempty"`
}

// LayerResult의 Errored는 "대조 자체를 수행하지 못했다"는 뜻이다.
// Pass=false이면서 Mismatches가 비어 있을 수 있는 유일한 경우이며,
// 이 층의 "불일치 0"을 통과로 읽으면 안 된다는 신호다(미검증 ≠ 통과).
// E2E 러너는 이 값이 true인 회차에는 어떤 셀도 확정하지 않는다.
type LayerResult struct {
	Name       string     `json:"name"`
	Ran        bool       `json:"ran"`
	Pass       bool       `json:"pass"`
	Errored    bool       `json:"errored,omitempty"`
	Err        string     `json:"err,omitempty"`
	Checked    int        `json:"checked"`
	Mismatches []Mismatch `json:"mismatches"`
	Note       string     `json:"note,omitempty"`
}

// fail은 조회 실패를 층 결과에 기록한다. Note만 남기고 Pass를 true로 두면
// 상위(E2E 러너·리포트)가 "대조했고 이상 없음"으로 오해한다.
func (lr *LayerResult) fail(note string, err error) {
	lr.Pass = false
	lr.Errored = true
	lr.Err = err.Error()
	if lr.Note == "" {
		lr.Note = note + err.Error()
	} else {
		lr.Note += " / " + note + err.Error()
	}
}

// PathDiff는 "불일치"가 아니라 **경로 간 설계 차이 관측**이다.
// NaN이 섞인 시간대는 MV 경로(필터 없음)와 checkvalue 폴백 경로(isFinite)가
// 서로 다른 통계를 만드는 것이 정상이므로, 결함으로 세지 않고 나란히 기록한다.
type PathDiff struct {
	CP       int64  `json:"cp"`
	Date     string `json:"date"`
	Hour     int    `json:"hour"`
	Field    string `json:"field"`
	NaNRows  int64  `json:"nanRows"`
	Finite   string `json:"finite"`   // finite 기준 기대(폴백 경로)
	All      string `json:"all"`      // 전체 기준 기대(MV 경로)
	Actual   string `json:"actual"`   // 실측
	Matched  string `json:"matched"`  // finite | all | none
	Layer    string `json:"layer"`
}

type Result struct {
	ReplicationDelay uint64        `json:"replicationDelay"`
	GuardPassed      bool          `json:"guardPassed"`
	ExcludeDates     []string      `json:"excludeDates"`
	L1Raw            LayerResult   `json:"l1Raw"`
	L1MV             LayerResult   `json:"l1Mv"`
	L2Daily          LayerResult   `json:"l2Daily"`
	L2Hourly         LayerResult   `json:"l2Hourly"`
	Warnings         []string      `json:"warnings"`
	PathDiffs        []PathDiff    `json:"pathDiffs,omitempty"`
	Pass             bool          `json:"pass"`
}

type Options struct {
	SkipL2      bool // 재생성 전이면 L2 생략
	MaxSamples  int  // 층별 mismatch 표본 상한 (0=20)
}

// Run은 시나리오 기대값과 실제 DB 상태를 대조한다.
func Run(ctx context.Context, p profile.Profile, s model.Scenario, opt Options) (*Result, error) {
	if opt.MaxSamples <= 0 {
		opt.MaxSamples = 20
	}
	res := &Result{}

	preview, err := planner.Build(s, time.Now())
	if err != nil {
		return nil, err
	}

	ch, err := chdb.Open(p)
	if err != nil {
		return nil, fmt.Errorf("ClickHouse 연결 실패: %w", err)
	}
	defer ch.Close()

	// 가드: 복제 지연 60초 초과 시 대조 중단 — 지연 복제본 대조는 대량 가짜 불일치(실측 34만 건)
	delay, err := ch.ReplicationDelay(ctx)
	if err != nil {
		res.Warnings = append(res.Warnings, "복제 지연 조회 실패(비복제 환경이면 무시): "+err.Error())
	}
	res.ReplicationDelay = delay
	if delay > 60 {
		res.GuardPassed = false
		return res, fmt.Errorf("복제 지연 %d초 > 60초 — 대조를 중단합니다. SYSTEM SYNC REPLICA 후 재시도하세요", delay)
	}
	res.GuardPassed = true

	disc, err := ch.Discover(ctx)
	if err != nil {
		return nil, err
	}
	res.ExcludeDates = disc.ExcludeDates
	for _, d := range disc.ExcludeDates {
		for _, de := range preview.Days {
			if de.Date == d {
				res.Warnings = append(res.Warnings,
					fmt.Sprintf("%s는 exclude_date에 등재되어 있습니다 — hourly가 checkvalue 폴백 경로로 생성되므로 경로 차이를 결함으로 오판하지 마세요", d))
			}
		}
	}

	loc, _ := s.Location()

	res.L1Raw = LayerResult{Name: "L1 expected ↔ checkvalue 재집계", Ran: true, Pass: true}
	res.L1MV = LayerResult{Name: "L1 checkvalue ↔ daily_stats(CH) MV", Ran: true, Pass: true}
	res.L2Daily = LayerResult{Name: "L2 expected ↔ MariaDB daily_stats", Pass: true}
	res.L2Hourly = LayerResult{Name: "L2 expected ↔ MariaDB hourly", Pass: true}

	var mdb *mariadb.Client
	if !opt.SkipL2 {
		mdb, err = mariadb.Open(p)
		if err != nil {
			return nil, fmt.Errorf("MariaDB 연결 실패: %w", err)
		}
		defer mdb.Close()
		res.L2Daily.Ran = true
		res.L2Hourly.Ran = true
	}

	for _, de := range preview.Days {
		day, _ := time.ParseInLocation("2006-01-02", de.Date, loc)
		from, to := day, day.AddDate(0, 0, 1)

		// ---- L1: 원본 재집계 ----
		// NaN이 섞인 시나리오는 폴백 경로와 같은 조건(isFinite)으로 재집계해야
		// finite 기준 기대값과 대조가 성립한다. NaN이 없으면 두 질의는 같은 결과다.
		raw, err := ch.RawHourly(ctx, de.CheckpointID, from, to)
		if err != nil {
			return nil, fmt.Errorf("checkvalue 재집계 실패(cp %d %s): %w", de.CheckpointID, de.Date, err)
		}
		rawByHour := map[int]chdb.HourAgg{}
		for _, a := range raw {
			rawByHour[a.Hour] = a
		}
		finiteByHour := rawByHour
		if de.HasNaN() {
			fin, ferr := ch.RawHourlyFinite(ctx, de.CheckpointID, from, to)
			if ferr != nil {
				return nil, fmt.Errorf("checkvalue finite 재집계 실패(cp %d %s): %w", de.CheckpointID, de.Date, ferr)
			}
			finiteByHour = map[int]chdb.HourAgg{}
			for _, a := range fin {
				finiteByHour[a.Hour] = a
			}
			// 같은 데이터를 두 조건으로 집계한 결과를 나란히 남긴다(경로 차이의 원천 증거).
			recordRawPathDiffs(res, de, rawByHour, finiteByHour)
		}
		compareHours(&res.L1Raw, "L1-raw", de, finiteByHour, chTolerance, true, opt.MaxSamples, false)

		// ---- L1: MV ----
		mvAgg, err := ch.MVHourly(ctx, de.CheckpointID, de.Date, disc.MVHasMaxTime)
		if err != nil {
			res.L1MV.fail("MV 조회 실패: ", err)
		} else {
			mvByHour := map[int]chdb.HourAgg{}
			for _, a := range mvAgg {
				mvByHour[a.Hour] = a
			}
			compareHours(&res.L1MV, "L1-mv", de, mvByHour, chTolerance, disc.MVHasMaxTime, opt.MaxSamples, true)
			if de.HasNaN() {
				recordMVPathDiffs(res, de, mvByHour)
			}
		}

		if opt.SkipL2 {
			continue
		}

		// ---- L2: MariaDB daily ----
		drow, err := mdb.DailyStats(ctx, de.CheckpointID, de.Date)
		if err != nil {
			return nil, fmt.Errorf("MariaDB daily_stats 조회 실패: %w", err)
		}
		compareDaily(&res.L2Daily, de, drow, opt.MaxSamples, res)

		// ---- L2: MariaDB hourly ----
		hrows, err := mdb.HourlyStats(ctx, de.CheckpointID, de.Date)
		if err != nil {
			// daily는 여기서 error를 반환해 시끄럽게 실패하고, MV는 Pass=false를 세운다.
			// hourly만 Note 문자열로 삼키면 "불일치 0 = 통과"가 되어 제품 hourly가 전부
			// 틀려 있어도 전 셀이 PASS로 확정된다. 반드시 Errored로 남긴다.
			res.L2Hourly.fail("hourly 조회 실패(보존 창: 당일+전일23시만 잔존): ", err)
		} else {
			compareHourly(&res.L2Hourly, de, hrows, time.Now(), loc, opt.MaxSamples, res)
		}
	}

	res.Pass = res.L1Raw.Pass && res.L1MV.Pass &&
		(!res.L2Daily.Ran || res.L2Daily.Pass) && (!res.L2Hourly.Ran || res.L2Hourly.Pass)
	return res, nil
}

func addMismatch(lr *LayerResult, m Mismatch, maxSamples int) {
	// Diff가 NaN/Inf면 결과 JSON 직렬화가 통째로 실패한다(리포트·상태 저장 유실).
	if math.IsNaN(m.Diff) || math.IsInf(m.Diff, 0) {
		m.Diff = 0
	}
	lr.Pass = false
	if len(lr.Mismatches) < maxSamples {
		lr.Mismatches = append(lr.Mismatches, m)
	}
}

// nullOrRaw는 해석하지 못한 max_time 원문을 실측값 칸에 보여준다.
// 빈 값이면 NULL, 아니면 "해석 불가: 2026-08-14 17:59:59" 처럼 원문을 남긴다.
func nullOrRaw(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "NULL"
	}
	return "해석 불가: " + raw
}

func near(a, b, tol float64) bool {
	return math.Abs(a-b) <= tol*math.Max(1, math.Max(math.Abs(a), math.Abs(b)))
}

// compareHours는 기대 시간대 통계와 CH 집계(raw 또는 MV)를 대조한다.
// skipNaNHours=true면 NaN이 섞인 시간대는 대조하지 않는다. MV 경로는 NaN을 필터하지
// 않으므로 finite 기준 기대값과 다른 것이 정상이며, 그 차이는 결함이 아니라
// 경로 차이 관측(PathDiff)으로 따로 남긴다.
func compareHours(lr *LayerResult, layer string, de model.DayExpected, actual map[int]chdb.HourAgg, tol float64, checkMaxTime bool, maxSamples int, skipNaNHours bool) {
	for _, he := range de.Hours {
		if skipNaNHours && he.HasNaN() {
			continue
		}
		exp := he.Stats
		act, ok := actual[he.Hour]
		lr.Checked++
		if exp.Count == 0 {
			if ok && act.Count > 0 {
				addMismatch(lr, Mismatch{Layer: layer, CP: de.CheckpointID, Date: de.Date, Hour: he.Hour,
					Field: "count", Expected: "0(생성 안 함)", Actual: fmt.Sprint(act.Count),
					Note: "NULL 시간대에 데이터 존재 — 기존 데이터와 겹쳤거나 오염"}, maxSamples)
			}
			continue
		}
		if !ok {
			addMismatch(lr, Mismatch{Layer: layer, CP: de.CheckpointID, Date: de.Date, Hour: he.Hour,
				Field: "row", Expected: fmt.Sprintf("count %d", exp.Count), Actual: "행 없음"}, maxSamples)
			continue
		}
		if act.Count != exp.Count {
			addMismatch(lr, Mismatch{Layer: layer, CP: de.CheckpointID, Date: de.Date, Hour: he.Hour,
				Field: "count", Expected: fmt.Sprint(exp.Count), Actual: fmt.Sprint(act.Count),
				Diff: float64(act.Count - exp.Count),
				Note: "count 차이 = 중복 적재 또는 결손 신호(L1에서만 검출 가능 — MariaDB엔 count 컬럼 없음)"}, maxSamples)
		}
		pairs := []struct {
			field    string
			exp, act float64
		}{{"min", exp.Min, act.Min}, {"max", exp.Max, act.Max}, {"avg", exp.Avg, act.Avg}}
		for _, pr := range pairs {
			if !near(pr.exp, pr.act, tol) {
				addMismatch(lr, Mismatch{Layer: layer, CP: de.CheckpointID, Date: de.Date, Hour: he.Hour,
					Field: pr.field, Expected: fmt.Sprintf("%.9g", pr.exp), Actual: fmt.Sprintf("%.9g", pr.act),
					Diff: math.Abs(pr.exp - pr.act)}, maxSamples)
			}
		}
		if checkMaxTime && exp.MaxTime != nil && act.MaxTime != nil {
			// 초 단위 대조(제품 저장 정밀도 기준, 실측 절차와 동일)
			if exp.MaxTime.Unix() != act.MaxTime.In(exp.MaxTime.Location()).Unix() {
				addMismatch(lr, Mismatch{Layer: layer, CP: de.CheckpointID, Date: de.Date, Hour: he.Hour,
					Field: "max_time", Expected: exp.MaxTS, Actual: act.MaxTime.Format("2006-01-02 15:04:05"),
					Note: "동률 규칙(earliest) 위반 또는 오염 신호"}, maxSamples)
			}
		}
	}
}

// compareDaily는 기대값과 MariaDB daily_stats 행을 대조한다.
// 매핑: CH hour H ↔ 컬럼 _{H+1}(종료 라벨). NULL 시간대는 컬럼 NULL이어야 함(0 오염 검출).
func compareDaily(lr *LayerResult, de model.DayExpected, drow *mariadb.DailyRow, maxSamples int, res *Result) {
	lr.Checked++
	if drow == nil {
		if de.RowCount > 0 {
			addMismatch(lr, Mismatch{Layer: "L2-daily", CP: de.CheckpointID, Date: de.Date, Hour: -1,
				Field: "row", Expected: "통계 행 존재", Actual: "행 없음",
				Note: "재생성 미실행이거나 게이트/멱등 스킵에 걸림 — 재생성 체크리스트 확인"}, maxSamples)
		}
		return
	}
	// 일별 대푯값 (avg = count 가중평균과 동치인 전체 sum/count)
	dayPairs := []struct {
		field string
		exp   model.Stats
		act   *float64
		expV  float64
	}{
		{"daily_min", de.Stats, drow.Min, de.Stats.Min},
		{"daily_max", de.Stats, drow.Max, de.Stats.Max},
		{"daily_avg", de.Stats, drow.Avg, de.Stats.Avg},
	}
	for _, pr := range dayPairs {
		lr.Checked++
		if de.Stats.Count == 0 {
			continue
		}
		if pr.act == nil {
			addMismatch(lr, Mismatch{Layer: "L2-daily", CP: de.CheckpointID, Date: de.Date, Hour: -1,
				Field: pr.field, Expected: fmt.Sprintf("%.9g", pr.expV), Actual: "NULL"}, maxSamples)
			continue
		}
		if !near(pr.expV, *pr.act, model.MariaTolerance) {
			addMismatch(lr, Mismatch{Layer: "L2-daily", CP: de.CheckpointID, Date: de.Date, Hour: -1,
				Field: pr.field, Expected: fmt.Sprintf("%.9g", pr.expV), Actual: fmt.Sprintf("%.9g", *pr.act),
				Diff: math.Abs(pr.expV - *pr.act)}, maxSamples)
		}
	}
	// max_time: epoch ms, 초 단위 대조
	if de.Stats.MaxTime != nil {
		lr.Checked++
		if drow.MaxTimeMs == nil {
			addMismatch(lr, Mismatch{Layer: "L2-daily", CP: de.CheckpointID, Date: de.Date, Hour: -1,
				Field: "max_time", Expected: de.Stats.MaxTS, Actual: nullOrRaw(drow.MaxTimeRaw),
				Note: "max_time이 NULL이거나 시각으로 해석되지 않아 대조 불가"}, maxSamples)
		} else if de.Stats.MaxTime.Unix() != *drow.MaxTimeMs/1000 {
			addMismatch(lr, Mismatch{Layer: "L2-daily", CP: de.CheckpointID, Date: de.Date, Hour: -1,
				Field: "max_time", Expected: de.Stats.MaxTS,
				Actual: time.UnixMilli(*drow.MaxTimeMs).In(de.Stats.MaxTime.Location()).Format("2006-01-02 15:04:05"),
				Note: "epoch ms, 초 단위 대조. 동률(earliest) 규칙 확인"}, maxSamples)
		}
	}
	// 시간별 컬럼
	for _, he := range de.Hours {
		lr.Checked++
		exp := he.Stats
		actMin, actMax, actAvg := drow.HourMin[he.Hour], drow.HourMax[he.Hour], drow.HourAvg[he.Hour]
		if exp.Count == 0 {
			// 빈 시간대 = NULL이어야 함. 0이 들어있으면 결함(실측 규칙)
			for field, act := range map[string]*float64{"min": actMin, "max": actMax, "avg": actAvg} {
				if act != nil {
					addMismatch(lr, Mismatch{Layer: "L2-daily", CP: de.CheckpointID, Date: de.Date, Hour: he.Hour,
						Field: field + he.DailyCol, Expected: "NULL", Actual: fmt.Sprintf("%.9g", *act),
						Note: "빈 시간대는 0이 아니라 NULL이어야 합니다"}, maxSamples)
				}
			}
			continue
		}
		if he.HasNaN() {
			// NaN 시간대는 제품이 어느 경로로 만들었느냐에 따라 정답이 갈린다.
			// 둘 중 하나와 맞으면 통과로 보고 "어느 경로였는지"를 기록한다.
			judgeNaNHour(res, lr, "L2-daily", de, he, actMin, actMax, actAvg, he.DailyCol, maxSamples)
			continue
		}
		hourPairs := []struct {
			field string
			expV  float64
			act   *float64
		}{{"min", exp.Min, actMin}, {"max", exp.Max, actMax}, {"avg", exp.Avg, actAvg}}
		for _, pr := range hourPairs {
			if pr.act == nil {
				addMismatch(lr, Mismatch{Layer: "L2-daily", CP: de.CheckpointID, Date: de.Date, Hour: he.Hour,
					Field: pr.field + he.DailyCol, Expected: fmt.Sprintf("%.9g", pr.expV), Actual: "NULL",
					Note: "값이 있어야 할 시간대가 NULL"}, maxSamples)
				continue
			}
			if !near(pr.expV, *pr.act, model.MariaTolerance) {
				addMismatch(lr, Mismatch{Layer: "L2-daily", CP: de.CheckpointID, Date: de.Date, Hour: he.Hour,
					Field: pr.field + he.DailyCol, Expected: fmt.Sprintf("%.9g", pr.expV),
					Actual: fmt.Sprintf("%.9g", *pr.act), Diff: math.Abs(pr.expV - *pr.act)}, maxSamples)
			}
		}
	}
}

// compareHourly는 기대값과 hourly_checkpoint_data_log를 대조한다.
// hour = 시작 라벨(H↔H, daily와 반대). 빈 시간대는 행 자체가 없어야 한다(NULL 아님).
// 보존 창(당일 생성완료분 + 전일 23시) 밖 시간대는 행 부재가 정상이므로 대조에서 제외하고,
// 반대로 창 밖에 행이 남아 있으면 truncate/삭제 미동작 신호로 잡는다.
func compareHourly(lr *LayerResult, de model.DayExpected, hrows []mariadb.HourlyRow, now time.Time, loc *time.Location, maxSamples int, res *Result) {
	byHour := map[int]mariadb.HourlyRow{}
	for _, hr := range hrows {
		byHour[hr.Hour] = hr
	}
	skipped := 0
	for _, he := range de.Hours {
		exp := he.Stats
		act, ok := byHour[he.Hour]
		if !hourlyInWindow(de.Date, he.Hour, now, loc) {
			if ok {
				lr.Checked++
				addMismatch(lr, Mismatch{Layer: "L2-hourly", CP: de.CheckpointID, Date: de.Date, Hour: he.Hour,
					Field: "row", Expected: "행 부재(보존 창 밖)", Actual: "행 존재",
					Note: "창 밖 잔존 행 — 00:05 truncate 미동작 또는 시계 이상 의심"}, maxSamples)
			} else {
				skipped++
			}
			continue
		}
		lr.Checked++
		if exp.Count == 0 {
			if ok {
				addMismatch(lr, Mismatch{Layer: "L2-hourly", CP: de.CheckpointID, Date: de.Date, Hour: he.Hour,
					Field: "row", Expected: "행 미생성", Actual: "행 존재",
					Note: "hourly의 빈 시간대는 NULL이 아니라 행 부재여야 합니다"}, maxSamples)
			}
			continue
		}
		if !ok {
			addMismatch(lr, Mismatch{Layer: "L2-hourly", CP: de.CheckpointID, Date: de.Date, Hour: he.Hour,
				Field: "row", Expected: fmt.Sprintf("count %d", exp.Count), Actual: "행 없음",
				Note: "보존 창 안인데 행이 없음 — 생성 실패/폴백 실패 또는 삭제 의심"}, maxSamples)
			continue
		}
		if he.HasNaN() {
			judgeNaNHour(res, lr, "L2-hourly", de, he, act.Min, act.Max, act.Avg, "", maxSamples)
			continue
		}
		pairs := []struct {
			field string
			expV  float64
			act   *float64
		}{{"min", exp.Min, act.Min}, {"max", exp.Max, act.Max}, {"avg", exp.Avg, act.Avg}}
		for _, pr := range pairs {
			if pr.act == nil {
				// daily(compareDaily)와 동일하게 결함으로 잡는다. 여기서 continue하면
				// 제품이 행은 만들되 값을 NULL로 쓰는 회귀가 "대조 0건 → 통과"가 된다.
				addMismatch(lr, Mismatch{Layer: "L2-hourly", CP: de.CheckpointID, Date: de.Date, Hour: he.Hour,
					Field: pr.field, Expected: fmt.Sprintf("%.9g", pr.expV), Actual: "NULL",
					Note: "값이 있어야 할 시간대가 NULL"}, maxSamples)
				continue
			}
			if !near(pr.expV, *pr.act, model.MariaTolerance) {
				addMismatch(lr, Mismatch{Layer: "L2-hourly", CP: de.CheckpointID, Date: de.Date, Hour: he.Hour,
					Field: pr.field, Expected: fmt.Sprintf("%.9g", pr.expV), Actual: fmt.Sprintf("%.9g", *pr.act),
					Diff: math.Abs(pr.expV - *pr.act)}, maxSamples)
			}
		}
		if exp.MaxTime != nil {
			lr.Checked++
			if act.MaxTimeMs == nil {
				// 파싱 실패·NULL을 조용히 건너뛰면 검증 항목 하나(동률 earliest 규칙)가
				// "검증했다는 흔적조차 없이" 빠진다. 원문을 실측값으로 남겨 진단 가능하게 한다.
				addMismatch(lr, Mismatch{Layer: "L2-hourly", CP: de.CheckpointID, Date: de.Date, Hour: he.Hour,
					Field: "max_time", Expected: exp.MaxTS, Actual: nullOrRaw(act.MaxTimeRaw),
					Note: "max_time이 NULL이거나 시각으로 해석되지 않아 대조 불가"}, maxSamples)
			} else if exp.MaxTime.Unix() != *act.MaxTimeMs/1000 {
				addMismatch(lr, Mismatch{Layer: "L2-hourly", CP: de.CheckpointID, Date: de.Date, Hour: he.Hour,
					Field: "max_time", Expected: exp.MaxTS,
					Actual: time.UnixMilli(*act.MaxTimeMs).In(exp.MaxTime.Location()).Format("2006-01-02 15:04:05")}, maxSamples)
			}
		}
	}
	if skipped > 0 {
		note := fmt.Sprintf("%s: 보존 창 밖 %d개 시간대 대조 제외(정상 부재)", de.Date, skipped)
		if lr.Note == "" {
			lr.Note = note
		} else {
			lr.Note += " / " + note
		}
	}
}

// statText는 기대/실측 값을 표시 문자열로 만든다(NaN 포함).
func statText(v float64, present bool) string {
	if !present {
		return "NULL"
	}
	if math.IsNaN(v) {
		return "NaN"
	}
	return fmt.Sprintf("%.9g", v)
}

func addDiff(res *Result, d PathDiff) {
	if res == nil {
		return
	}
	res.PathDiffs = append(res.PathDiffs, d)
}

// recordRawPathDiffs는 같은 checkvalue 데이터를 두 조건(전체 / isFinite)으로 집계한
// 결과를 나란히 남긴다. 제품 두 경로가 갈리는 이유의 원천 증거다.
func recordRawPathDiffs(res *Result, de model.DayExpected, all, finite map[int]chdb.HourAgg) {
	for _, he := range de.Hours {
		if !he.HasNaN() {
			continue
		}
		a, aok := all[he.Hour]
		f, fok := finite[he.Hour]
		addDiff(res, PathDiff{Layer: "L1-raw", CP: de.CheckpointID, Date: de.Date, Hour: he.Hour,
			Field: "count", NaNRows: he.StatsAll.NaNCount,
			Finite: statText(float64(he.Stats.Count), true), All: statText(float64(he.StatsAll.Count), true),
			Actual: fmt.Sprintf("전체 %s / isFinite %s", statText(float64(a.Count), aok), statText(float64(f.Count), fok)),
			Matched: "-"})
		addDiff(res, PathDiff{Layer: "L1-raw", CP: de.CheckpointID, Date: de.Date, Hour: he.Hour,
			Field: "avg", NaNRows: he.StatsAll.NaNCount,
			Finite: statText(he.Stats.Avg, true), All: statText(he.StatsAll.Avg, true),
			Actual: fmt.Sprintf("전체 %s / isFinite %s", statText(a.Avg, aok), statText(f.Avg, fok)),
			Matched: "-"})
	}
}

// recordMVPathDiffs는 NaN 시간대의 MV 실측을 두 기대와 나란히 남긴다.
// MV 경로가 NaN을 세는지(전체 기준) 거르는지(finite 기준)가 여기서 드러난다.
func recordMVPathDiffs(res *Result, de model.DayExpected, mv map[int]chdb.HourAgg) {
	for _, he := range de.Hours {
		if !he.HasNaN() {
			continue
		}
		a, ok := mv[he.Hour]
		fields := []struct {
			name             string
			fin, all, actual float64
		}{
			{"count", float64(he.Stats.Count), float64(he.StatsAll.Count), float64(a.Count)},
			{"avg", he.Stats.Avg, he.StatsAll.Avg, a.Avg},
			{"min", he.Stats.Min, he.StatsAll.Min, a.Min},
			{"max", he.Stats.Max, he.StatsAll.Max, a.Max},
		}
		for _, f := range fields {
			addDiff(res, PathDiff{Layer: "L1-mv", CP: de.CheckpointID, Date: de.Date, Hour: he.Hour,
				Field: f.name, NaNRows: he.StatsAll.NaNCount,
				Finite: statText(f.fin, true), All: statText(f.all, true),
				Actual: statText(f.actual, ok), Matched: whichMatched(f.fin, f.all, f.actual, ok, chTolerance)})
		}
	}
}

// whichMatched는 실측이 어느 기대와 맞는지 알려준다. NaN은 등호로 비교할 수 없으므로
// "둘 다 NaN이면 일치"로 따로 처리한다.
func whichMatched(fin, all, actual float64, present bool, tol float64) string {
	if !present {
		return "none"
	}
	switch {
	case sameVal(fin, actual, tol) && sameVal(all, actual, tol):
		return "both"
	case sameVal(fin, actual, tol):
		return "finite"
	case sameVal(all, actual, tol):
		return "all"
	}
	return "none"
}

func sameVal(a, b, tol float64) bool {
	if math.IsNaN(a) || math.IsNaN(b) {
		return math.IsNaN(a) && math.IsNaN(b)
	}
	return near(a, b, tol)
}

// judgeNaNHour는 NaN이 섞인 시간대의 MariaDB 실측을 판정한다.
// 두 기대(폴백 경로 / MV 경로) 중 하나와 맞으면 통과이며, 어느 쪽이었는지 기록한다.
// 둘 다 아니면 그때는 진짜 불일치다.
func judgeNaNHour(res *Result, lr *LayerResult, layer string, de model.DayExpected, he model.HourExpected,
	actMin, actMax, actAvg *float64, colSuffix string, maxSamples int) {
	get := func(p *float64) (float64, bool) {
		if p == nil {
			return 0, false
		}
		return *p, true
	}
	fields := []struct {
		name     string
		fin, all float64
		act      *float64
	}{
		{"min", he.Stats.Min, he.StatsAll.Min, actMin},
		{"max", he.Stats.Max, he.StatsAll.Max, actMax},
		{"avg", he.Stats.Avg, he.StatsAll.Avg, actAvg},
	}
	for _, f := range fields {
		v, ok := get(f.act)
		lr.Checked++
		matched := whichMatched(f.fin, f.all, v, ok, model.MariaTolerance)
		addDiff(res, PathDiff{Layer: layer, CP: de.CheckpointID, Date: de.Date, Hour: he.Hour,
			Field: f.name + colSuffix, NaNRows: he.StatsAll.NaNCount,
			Finite: statText(f.fin, true), All: statText(f.all, true),
			Actual: statText(v, ok), Matched: matched})
		if matched == "none" {
			addMismatch(lr, Mismatch{Layer: layer, CP: de.CheckpointID, Date: de.Date, Hour: he.Hour,
				Field: f.name + colSuffix,
				Expected: fmt.Sprintf("%s(폴백 경로) 또는 %s(MV 경로)", statText(f.fin, true), statText(f.all, true)),
				Actual:   statText(v, ok),
				Note:     "NaN 시간대: 두 경로 기대값 어느 쪽과도 일치하지 않음"}, maxSamples)
		}
	}
}

// ExpectedFor는 기대값만 필요할 때 쓴다.
func ExpectedFor(s model.Scenario) (*model.Preview, error) {
	return planner.Build(s, time.Now())
}

var _ = generator.StatsFor // (참조 유지)
