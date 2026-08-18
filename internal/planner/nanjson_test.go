package planner

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"oqt325/internal/model"
)

// NaN이 섞인 preview는 반드시 JSON으로 나가야 한다.
// encoding/json은 NaN을 마샬링하지 못해, 처리하지 않으면 Preview 응답 전체가
// 실패하고 화면이 비어버린다(기능이 조용히 죽는 경로).
func TestPreviewWithNaNMarshals(t *testing.T) {
	s := model.Scenario{
		Name: "nan", CheckpointIDs: []int64{100},
		StartDate: "2026-08-14", EndDate: "2026-08-14", Timezone: "Asia/Seoul",
		IntervalSec: 60, Daily: model.Goal{Min: 20, Max: 40, Avg: 30},
		Overrides: []model.HourOverride{
			{Hour: 0, Mode: "goal", Goal: model.Goal{Min: 20, Max: 40, Avg: 30}, NaNCount: 3},
		},
		Seed: 325, BatchSize: 1000, DriverCode: "numeric_raw",
	}
	pv, err := Build(s, time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}

	h0 := pv.Days[0].Hours[0]
	if h0.StatsAll.NaNCount != 3 {
		t.Fatalf("NaN 수 = %d, want 3", h0.StatsAll.NaNCount)
	}
	if !math.IsNaN(h0.StatsAll.Avg) {
		t.Fatalf("전체 기준 avg = %g, want NaN", h0.StatsAll.Avg)
	}

	b, err := json.Marshal(pv)
	if err != nil {
		t.Fatalf("preview 직렬화 실패: %v", err)
	}
	if !strings.Contains(string(b), `"NaN"`) {
		t.Error("NaN이 JSON에 표현되지 않았다")
	}

	// 왕복도 되어야 한다(리포트 저장/재로드 경로)
	var back model.Preview
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("역직렬화 실패: %v", err)
	}
	if !math.IsNaN(back.Days[0].Hours[0].StatsAll.Avg) {
		t.Error("왕복 후 NaN이 보존되지 않았다")
	}
	if back.Days[0].Hours[0].Stats.Count != h0.Stats.Count {
		t.Error("왕복 후 finite count가 달라졌다")
	}
}

// NaN이 없으면 두 기대값이 같고, 기존 JSON 표현이 그대로여야 한다.
func TestPreviewWithoutNaNUnchanged(t *testing.T) {
	s := model.Scenario{
		Name: "plain", CheckpointIDs: []int64{100},
		StartDate: "2026-08-14", EndDate: "2026-08-14", Timezone: "Asia/Seoul",
		IntervalSec: 60, Daily: model.Goal{Min: 20, Max: 40, Avg: 30},
		Seed: 325, BatchSize: 1000, DriverCode: "numeric_raw",
	}
	pv, err := Build(s, time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	h := pv.Days[0].Hours[5]
	if h.Stats.Avg != h.StatsAll.Avg || h.Stats.Count != h.StatsAll.Count {
		t.Errorf("NaN이 없는데 두 기준이 다르다: %+v vs %+v", h.Stats, h.StatsAll)
	}
	b, err := json.Marshal(pv)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"NaN"`) {
		t.Error("NaN이 없는 시나리오인데 NaN 표현이 들어갔다")
	}
}
