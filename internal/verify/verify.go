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

type LayerResult struct {
	Name       string     `json:"name"`
	Ran        bool       `json:"ran"`
	Pass       bool       `json:"pass"`
	Checked    int        `json:"checked"`
	Mismatches []Mismatch `json:"mismatches"`
	Note       string     `json:"note,omitempty"`
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
		raw, err := ch.RawHourly(ctx, de.CheckpointID, from, to)
		if err != nil {
			return nil, fmt.Errorf("checkvalue 재집계 실패(cp %d %s): %w", de.CheckpointID, de.Date, err)
		}
		rawByHour := map[int]chdb.HourAgg{}
		for _, a := range raw {
			rawByHour[a.Hour] = a
		}
		compareHours(&res.L1Raw, "L1-raw", de, rawByHour, chTolerance, true, opt.MaxSamples)

		// ---- L1: MV ----
		mvAgg, err := ch.MVHourly(ctx, de.CheckpointID, de.Date, disc.MVHasMaxTime)
		if err != nil {
			res.L1MV.Note = "MV 조회 실패: " + err.Error()
			res.L1MV.Pass = false
		} else {
			mvByHour := map[int]chdb.HourAgg{}
			for _, a := range mvAgg {
				mvByHour[a.Hour] = a
			}
			compareHours(&res.L1MV, "L1-mv", de, mvByHour, chTolerance, disc.MVHasMaxTime, opt.MaxSamples)
		}

		if opt.SkipL2 {
			continue
		}

		// ---- L2: MariaDB daily ----
		drow, err := mdb.DailyStats(ctx, de.CheckpointID, de.Date)
		if err != nil {
			return nil, fmt.Errorf("MariaDB daily_stats 조회 실패: %w", err)
		}
		compareDaily(&res.L2Daily, de, drow, opt.MaxSamples)

		// ---- L2: MariaDB hourly ----
		hrows, err := mdb.HourlyStats(ctx, de.CheckpointID, de.Date)
		if err != nil {
			res.L2Hourly.Note = "hourly 조회 실패(보존 창: 당일+전일23시만 잔존): " + err.Error()
		} else {
			compareHourly(&res.L2Hourly, de, hrows, time.Now(), loc, opt.MaxSamples)
		}
	}

	res.Pass = res.L1Raw.Pass && res.L1MV.Pass &&
		(!res.L2Daily.Ran || res.L2Daily.Pass) && (!res.L2Hourly.Ran || res.L2Hourly.Pass)
	return res, nil
}

func addMismatch(lr *LayerResult, m Mismatch, maxSamples int) {
	lr.Pass = false
	if len(lr.Mismatches) < maxSamples {
		lr.Mismatches = append(lr.Mismatches, m)
	}
}

func near(a, b, tol float64) bool {
	return math.Abs(a-b) <= tol*math.Max(1, math.Max(math.Abs(a), math.Abs(b)))
}

// compareHours는 기대 시간대 통계와 CH 집계(raw 또는 MV)를 대조한다.
func compareHours(lr *LayerResult, layer string, de model.DayExpected, actual map[int]chdb.HourAgg, tol float64, checkMaxTime bool, maxSamples int) {
	for _, he := range de.Hours {
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
func compareDaily(lr *LayerResult, de model.DayExpected, drow *mariadb.DailyRow, maxSamples int) {
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
	if de.Stats.MaxTime != nil && drow.MaxTimeMs != nil {
		lr.Checked++
		if de.Stats.MaxTime.Unix() != *drow.MaxTimeMs/1000 {
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
func compareHourly(lr *LayerResult, de model.DayExpected, hrows []mariadb.HourlyRow, now time.Time, loc *time.Location, maxSamples int) {
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
		pairs := []struct {
			field string
			expV  float64
			act   *float64
		}{{"min", exp.Min, act.Min}, {"max", exp.Max, act.Max}, {"avg", exp.Avg, act.Avg}}
		for _, pr := range pairs {
			if pr.act == nil {
				continue
			}
			if !near(pr.expV, *pr.act, model.MariaTolerance) {
				addMismatch(lr, Mismatch{Layer: "L2-hourly", CP: de.CheckpointID, Date: de.Date, Hour: he.Hour,
					Field: pr.field, Expected: fmt.Sprintf("%.9g", pr.expV), Actual: fmt.Sprintf("%.9g", *pr.act),
					Diff: math.Abs(pr.expV - *pr.act)}, maxSamples)
			}
		}
		if exp.MaxTime != nil && act.MaxTimeMs != nil {
			if exp.MaxTime.Unix() != *act.MaxTimeMs/1000 {
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

// ExpectedFor는 기대값만 필요할 때 쓴다.
func ExpectedFor(s model.Scenario) (*model.Preview, error) {
	return planner.Build(s, time.Now())
}

var _ = generator.StatsFor // (참조 유지)
