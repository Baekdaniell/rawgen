// Package generator는 목표 min/max/avg를 만족하는 결정적 수열을 생성한다.
// 같은 (seed, checkpoint, 날짜, hour) 입력은 항상 같은 수열을 만든다.
package generator

import (
	"fmt"
	"hash/fnv"
	"math"
	"math/rand"
	"sort"
	"time"

	"oqt325/internal/model"
)

// SeedFor는 (base seed, cp, 날짜, hour) 조합을 FNV-1a로 혼합해 파생 seed를 만든다.
// 단순 합산은 (1일,hour2)=(2일,hour1) 충돌을 만들므로 금지.
func SeedFor(base int64, cp int64, date string, hour int) int64 {
	h := fnv.New64a()
	fmt.Fprintf(h, "%d|%d|%s|%d", base, cp, date, hour)
	return int64(h.Sum64())
}

// ValidateGoal은 count 특수 규칙까지 포함해 목표를 검증한다.
func ValidateGoal(g model.Goal, count int, label string) error {
	if count <= 0 {
		return nil
	}
	at := ""
	if label != "" {
		at = "[" + label + "] "
	}
	if g.Min > g.Max {
		return fmt.Errorf("%smin은 max보다 클 수 없습니다", at)
	}
	if g.Avg < g.Min || g.Avg > g.Max {
		return fmt.Errorf("%savg는 min과 max 사이여야 합니다", at)
	}
	if count == 1 && (g.Min != g.Max || g.Max != g.Avg) {
		return fmt.Errorf("%scount=1이면 min=max=avg여야 합니다", at)
	}
	if count == 2 && math.Abs(g.Avg-(g.Min+g.Max)/2) > 1e-9*math.Max(1, math.Abs(g.Avg)) {
		return fmt.Errorf("%scount=2이면 avg는 (min+max)/2여야 합니다", at)
	}
	return nil
}

// Values는 목표를 만족하는 오름차순 수열과 잔차를 반환한다.
// 잔차 보정은 여러 샘플에 분산 + [min,max] 클램프 — 마지막 1개 몰아주기는
// 보정값이 경계를 이탈해 min/max 기대값을 깨뜨린다(계획서 §6.2).
func Values(count int, g model.Goal, seed int64, label string) ([]float64, float64, error) {
	if err := ValidateGoal(g, count, label); err != nil {
		return nil, 0, err
	}
	switch {
	case count == 0:
		return nil, 0, nil
	case count == 1:
		return []float64{g.Avg}, 0, nil
	case count == 2:
		return []float64{g.Min, g.Max}, 0, nil
	}

	rng := rand.New(rand.NewSource(seed))
	values := make([]float64, 0, count)
	values = append(values, g.Min, g.Max)
	base := (float64(count)*g.Avg - g.Min - g.Max) / float64(count-2)
	span := math.Min(math.Min(g.Max-base, base-g.Min), g.Max-g.Min)
	if span < 0 {
		span = 0
	}
	for i := 0; i < count-2; i++ {
		jitter := (rng.Float64() - 0.5) * span * 0.08
		v := base + jitter
		if v < g.Min {
			v = g.Min
		}
		if v > g.Max {
			v = g.Max
		}
		values = append(values, v)
	}

	targetSum := float64(count) * g.Avg
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	delta := targetSum - sum
	for pass := 0; pass < 4 && math.Abs(delta) > 1e-9; pass++ {
		for i := 2; i < len(values) && math.Abs(delta) > 1e-9; i++ {
			var room float64
			if delta > 0 {
				room = g.Max - values[i]
			} else {
				room = values[i] - g.Min
			}
			step := math.Copysign(math.Min(math.Abs(delta), room), delta)
			values[i] += step
			delta -= step
		}
	}

	sort.Float64s(values)
	return values, delta, nil
}

// FormatData는 driver_code별 data 문자열을 만든다.
// NaN은 포맷 접미사 없이 "NaN"으로 적는다(제품이 문자열을 파싱하는 경로에서
// "NaN %" 같은 값이 또 다른 변수가 되지 않도록).
func FormatData(v float64, driverCode string) string {
	if math.IsNaN(v) {
		return "NaN"
	}
	if driverCode == "numeric_percent" {
		return fmt.Sprintf("%.3f %%", v)
	}
	return fmt.Sprintf("%.6f", v)
}

// HourRows는 특정 (cp, day, hour)의 checkvalue 행을 생성한다.
func HourRows(s *model.Scenario, cp int64, day time.Time, hour int, loc *time.Location) ([]model.Row, float64, error) {
	goal := s.GoalForHour(hour)
	if goal == nil {
		return nil, 0, nil
	}
	total := 3600 / s.IntervalSec
	nanCount := s.NaNCountForHour(hour)
	if nanCount < 0 {
		nanCount = 0
	}
	if nanCount >= total {
		return nil, 0, fmt.Errorf("hour %d: NaN %d행이 전체 행 수(%d) 이상입니다", hour, nanCount, total)
	}
	// 정상 행은 NaN을 뺀 나머지 개수로 goal을 만족시킨다. 그래야 finite 기준 기대값이
	// 목표와 정확히 일치하고, NaN은 "섞인 오염"으로만 작용한다.
	count := total - nanCount
	dateText := day.Format("2006-01-02")
	seed := SeedFor(s.Seed, cp, dateText, hour)
	values, residual, err := Values(count, *goal, seed, fmt.Sprintf("%s hour %d", dateText, hour))
	if err != nil {
		return nil, 0, err
	}
	if nanCount > 0 {
		values = scatterNaN(values, nanCount, seed)
	}
	hourStart := time.Date(day.Year(), day.Month(), day.Day(), hour, 0, 0, 0, loc)
	rows := make([]model.Row, len(values))
	for i, v := range values {
		ts := hourStart.Add(time.Duration(i*s.IntervalSec) * time.Second)
		rows[i] = model.Row{
			LogDate:      ts,
			LogDateText:  ts.Format("2006-01-02 15:04:05.000"),
			CheckpointID: cp,
			RawData:      v,
			Data:         FormatData(v, s.DriverCode),
		}
	}
	return rows, residual, nil
}

// scatterNaN은 정상 값 사이에 NaN을 흩뿌린다. 앞뒤로 몰리면 제품의 부분 집계·
// 경계 처리와 얽혀 원인 분리가 어려워지므로 seed 기반으로 고르게 섞는다.
// 반환 길이 = len(values) + nanCount.
func scatterNaN(values []float64, nanCount int, seed int64) []float64 {
	total := len(values) + nanCount
	rng := rand.New(rand.NewSource(seed ^ 0x4e614e)) // "NaN"
	// total개 자리 중 nanCount개를 균등 간격 + 지터로 고른다(결정적).
	slots := make([]bool, total)
	step := float64(total) / float64(nanCount)
	for i := 0; i < nanCount; i++ {
		pos := int(step*float64(i) + rng.Float64()*step)
		for pos >= total {
			pos--
		}
		for slots[pos] {
			pos = (pos + 1) % total
		}
		slots[pos] = true
	}
	out := make([]float64, 0, total)
	vi := 0
	for i := 0; i < total; i++ {
		if slots[i] {
			out = append(out, math.NaN())
			continue
		}
		out = append(out, values[vi])
		vi++
	}
	return out
}

// StatsFor는 행 목록의 finite 기준 통계다(NaN 제외).
// 제품의 checkvalue 폴백 경로(WHERE isFinite(raw_data))가 만들어야 할 값이며,
// NaN이 없으면 StatsAllFor와 같다. max_time 동률 시 earliest(제품 규칙과 동일).
func StatsFor(rows []model.Row) model.Stats {
	return statsOf(rows, true)
}

// StatsAllFor는 전체 기준 통계다(NaN 포함). 제품의 MV 경로(countState/avgState에
// 필터가 없음)가 만들어야 할 값이며, NaN이 하나라도 있으면 Avg는 NaN이 된다.
func StatsAllFor(rows []model.Row) model.Stats {
	return statsOf(rows, false)
}

func statsOf(rows []model.Row, finiteOnly bool) model.Stats {
	if len(rows) == 0 {
		return model.Stats{}
	}
	st := model.Stats{Min: math.Inf(1), Max: math.Inf(-1)}
	var nan int64
	for i := range rows {
		v := rows[i].RawData
		if math.IsNaN(v) {
			nan++
			if finiteOnly {
				continue // 폴백 경로는 isFinite로 걸러낸다
			}
			// 전체 기준: count에 포함되고 합계를 오염시킨다(avg가 NaN이 된다).
			st.Count++
			st.Sum += v
			continue
		}
		st.Count++
		if v < st.Min {
			st.Min = v
		}
		st.Sum += v
		if v > st.Max { // 초과 조건 = 동률 시 earliest 유지
			st.Max = v
			t := rows[i].LogDate
			st.MaxTime = &t
		}
	}
	st.NaNCount = nan
	if st.Count == 0 {
		return model.Stats{NaNCount: nan}
	}
	st.Avg = st.Sum / float64(st.Count)
	if st.MaxTime != nil {
		st.MaxTS = st.MaxTime.Format("2006-01-02 15:04:05.000")
	}
	return st
}
