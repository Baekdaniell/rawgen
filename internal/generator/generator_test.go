package generator

import (
	"math"
	"testing"
	"time"

	"rawgen/internal/model"
)

func TestValuesMeetGoal(t *testing.T) {
	g := model.Goal{Min: 20, Max: 40, Avg: 30}
	values, residual, err := Values(360, g, SeedFor(325, 660070, "2026-08-11", 3), "t")
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(residual) > 1e-6 {
		t.Fatalf("residual too large: %g", residual)
	}
	min, max, sum := math.Inf(1), math.Inf(-1), 0.0
	for _, v := range values {
		if v < g.Min-1e-12 || v > g.Max+1e-12 {
			t.Fatalf("value out of range: %g", v)
		}
		min = math.Min(min, v)
		max = math.Max(max, v)
		sum += v
	}
	if min != g.Min || max != g.Max {
		t.Fatalf("min/max not exact: %g %g", min, max)
	}
	if avg := sum / float64(len(values)); math.Abs(avg-g.Avg) > 1e-6 {
		t.Fatalf("avg off: %g", avg)
	}
}

func TestValuesDeterministic(t *testing.T) {
	g := model.Goal{Min: 0, Max: 10, Avg: 5}
	a, _, _ := Values(100, g, 42, "")
	b, _, _ := Values(100, g, 42, "")
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("not deterministic at %d", i)
		}
	}
}

func TestSeedNoCollision(t *testing.T) {
	// (1일, hour2) != (2일, hour1) — 단순 합산 seed의 충돌 케이스
	if SeedFor(325, 1, "2026-08-01", 2) == SeedFor(325, 1, "2026-08-02", 1) {
		t.Fatal("seed collision")
	}
}

func TestValuesEdgeCounts(t *testing.T) {
	if v, _, err := Values(1, model.Goal{Min: 5, Max: 5, Avg: 5}, 1, ""); err != nil || v[0] != 5 {
		t.Fatalf("n=1: %v %v", v, err)
	}
	if _, _, err := Values(1, model.Goal{Min: 5, Max: 6, Avg: 5}, 1, ""); err == nil {
		t.Fatal("n=1 invalid should fail")
	}
	if v, _, err := Values(2, model.Goal{Min: 10, Max: 20, Avg: 15}, 1, ""); err != nil || v[0] != 10 || v[1] != 20 {
		t.Fatalf("n=2: %v %v", v, err)
	}
	if _, _, err := Values(2, model.Goal{Min: 10, Max: 20, Avg: 12}, 1, ""); err == nil {
		t.Fatal("n=2 invalid avg should fail")
	}
}

func TestInfeasibleAvgReportsResidual(t *testing.T) {
	// min 20 / max 40 / avg 20.01: max 1건이 평균을 끌어올려 도달 불가 → 잔차 보고
	_, residual, err := Values(360, model.Goal{Min: 20, Max: 40, Avg: 20.01}, 7, "")
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(residual) < 1 {
		t.Fatalf("expected large residual, got %g", residual)
	}
}

func TestStatsForEarliestTie(t *testing.T) {
	loc := time.UTC
	mk := func(h int, v float64) model.Row {
		ts := time.Date(2026, 8, 11, h, 0, 0, 0, loc)
		return model.Row{LogDate: ts, RawData: v}
	}
	st := StatsFor([]model.Row{mk(1, 5), mk(2, 9), mk(3, 9)})
	if st.MaxTime == nil || st.MaxTime.Hour() != 2 {
		t.Fatalf("earliest tie broken: %v", st.MaxTime)
	}
	empty := StatsFor(nil)
	if empty.Count != 0 {
		t.Fatal("empty stats")
	}
}

func TestHourLabels(t *testing.T) {
	// daily는 종료 라벨(_H+1), hourly는 시작 라벨(H) — 반대 컨벤션
	if model.DailyColSuffix(0) != "_01" || model.DailyColSuffix(23) != "_24" {
		t.Fatal("daily suffix")
	}
	if model.HourlyLabel(0) != "00" || model.HourlyLabel(23) != "23" {
		t.Fatal("hourly label")
	}
}
