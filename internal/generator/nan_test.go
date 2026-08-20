package generator

import (
	"math"
	"testing"
	"time"

	"rawgen/internal/model"
)

func nanScenario(nanCount int) *model.Scenario {
	return &model.Scenario{
		Name:          "nan",
		CheckpointIDs: []int64{100},
		StartDate:     "2026-08-14",
		EndDate:       "2026-08-14",
		Timezone:      "Asia/Seoul",
		IntervalSec:   60, // 시간당 60행
		Daily:         model.Goal{Min: 20, Max: 40, Avg: 30},
		Overrides:     []model.HourOverride{{Hour: 10, Mode: "goal", Goal: model.Goal{Min: 20, Max: 40, Avg: 30}, NaNCount: nanCount}},
		Seed:          325,
		BatchSize:     1000,
		DriverCode:    "numeric_raw",
	}
}

// NaN을 섞어도 전체 행 수는 그대로고, 정상 행은 goal을 정확히 만족해야 한다.
// (그래야 finite 기준 기대값이 폴백 경로의 정답이 된다.)
func TestHourRowsNaNKeepsRowCountAndGoal(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Seoul")
	s := nanScenario(5)
	day := time.Date(2026, 8, 14, 0, 0, 0, 0, loc)

	rows, _, err := HourRows(s, 100, day, 10, loc)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 60 {
		t.Fatalf("행 수 = %d, want 60 (NaN이 행을 대체할 뿐 총량은 그대로)", len(rows))
	}

	fin := StatsFor(rows)
	all := StatsAllFor(rows)

	if fin.NaNCount != 5 || all.NaNCount != 5 {
		t.Errorf("NaN 수 = finite %d / all %d, want 5", fin.NaNCount, all.NaNCount)
	}
	if fin.Count != 55 {
		t.Errorf("finite count = %d, want 55 (NaN 제외)", fin.Count)
	}
	if all.Count != 60 {
		t.Errorf("전체 count = %d, want 60 (NaN 포함)", all.Count)
	}
	if math.Abs(fin.Min-20) > 1e-9 || math.Abs(fin.Max-40) > 1e-9 {
		t.Errorf("finite min/max = %g/%g, want 20/40", fin.Min, fin.Max)
	}
	if math.Abs(fin.Avg-30) > 1e-6 {
		t.Errorf("finite avg = %g, want 30", fin.Avg)
	}
	if !math.IsNaN(all.Avg) {
		t.Errorf("전체 기준 avg = %g, want NaN (MV 경로는 NaN을 평균에 넣는다)", all.Avg)
	}
	if fin.MaxTime == nil {
		t.Error("finite max_time이 없다")
	}
}

// NaN이 앞뒤로 몰리지 않고 흩어져야 한다.
func TestNaNScattered(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Seoul")
	s := nanScenario(6)
	day := time.Date(2026, 8, 14, 0, 0, 0, 0, loc)
	rows, _, err := HourRows(s, 100, day, 10, loc)
	if err != nil {
		t.Fatal(err)
	}
	var idx []int
	for i, r := range rows {
		if math.IsNaN(r.RawData) {
			idx = append(idx, i)
		}
	}
	if len(idx) != 6 {
		t.Fatalf("NaN 행 %d개, want 6", len(idx))
	}
	// 연속으로 6개가 붙어 있으면 흩뿌리기가 실패한 것이다
	run := 1
	for i := 1; i < len(idx); i++ {
		if idx[i] == idx[i-1]+1 {
			run++
		} else {
			run = 1
		}
		if run >= 4 {
			t.Errorf("NaN이 %v에 몰려 있다", idx)
			break
		}
	}
	// data 문자열은 접미사 없이 NaN
	if got := FormatData(math.NaN(), "numeric_percent"); got != "NaN" {
		t.Errorf("FormatData(NaN) = %q, want \"NaN\"", got)
	}
}

// 같은 seed면 NaN 위치까지 재현되어야 한다(재개·재실행에서 기대값이 흔들리면 안 됨).
func TestNaNDeterministic(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Seoul")
	s := nanScenario(4)
	day := time.Date(2026, 8, 14, 0, 0, 0, 0, loc)
	a, _, _ := HourRows(s, 100, day, 10, loc)
	b, _, _ := HourRows(s, 100, day, 10, loc)
	for i := range a {
		an, bn := math.IsNaN(a[i].RawData), math.IsNaN(b[i].RawData)
		if an != bn {
			t.Fatalf("행 %d의 NaN 여부가 재현되지 않는다", i)
		}
		if !an && a[i].RawData != b[i].RawData {
			t.Fatalf("행 %d 값이 재현되지 않는다", i)
		}
	}
}

// NaN이 없으면 두 기준 통계는 완전히 같아야 한다(기존 동작 불변).
func TestNoNaNKeepsBothStatsIdentical(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Seoul")
	s := nanScenario(0)
	day := time.Date(2026, 8, 14, 0, 0, 0, 0, loc)
	rows, _, err := HourRows(s, 100, day, 10, loc)
	if err != nil {
		t.Fatal(err)
	}
	fin, all := StatsFor(rows), StatsAllFor(rows)
	if fin.Count != all.Count || fin.Min != all.Min || fin.Max != all.Max || fin.Avg != all.Avg {
		t.Errorf("NaN이 없는데 두 기준이 다르다: %+v vs %+v", fin, all)
	}
	if fin.NaNCount != 0 {
		t.Errorf("NaNCount = %d, want 0", fin.NaNCount)
	}
}

// NaN 행 수가 전체 이상이면 시나리오 검증에서 막아야 한다.
func TestScenarioRejectsExcessiveNaN(t *testing.T) {
	s := nanScenario(60) // 시간당 60행 전부 NaN
	if _, err := s.Validate(time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)); err == nil {
		t.Error("NaN 행 수가 전체와 같은데 통과했다")
	}
	s2 := nanScenario(5)
	s2.Overrides[0].Mode = "null"
	if _, err := s2.Validate(time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)); err == nil {
		t.Error("생성 안 함 시간대에 NaN을 넣었는데 통과했다")
	}
}
