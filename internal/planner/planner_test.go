package planner

import (
	"math"
	"testing"
	"time"

	"rawgen/internal/model"
)

func scenario() model.Scenario {
	return model.Scenario{
		CheckpointIDs: []int64{660070},
		StartDate:     "2026-08-10",
		EndDate:       "2026-08-11",
		Timezone:      "Asia/Seoul",
		IntervalSec:   10,
		Daily:         model.Goal{Min: 20, Max: 40, Avg: 30},
		Overrides: []model.HourOverride{
			{Hour: 2, Mode: "null"},
			{Hour: 15, Mode: "goal", Goal: model.Goal{Min: 28, Max: 40, Avg: 34}},
		},
		Seed:       325,
		DriverCode: "numeric_percent",
	}
}

func fixedNow() time.Time {
	loc, _ := time.LoadLocation("Asia/Seoul")
	return time.Date(2026, 8, 12, 12, 0, 0, 0, loc)
}

func TestBuildPerDay(t *testing.T) {
	p, err := Build(scenario(), fixedNow())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Days) != 2 {
		t.Fatalf("2일 범위인데 days=%d", len(p.Days))
	}
	// 날짜별로 따로 계산: 23시간 × 360건 (hour2는 NULL)
	for _, d := range p.Days {
		if d.RowCount != 23*360 {
			t.Fatalf("%s rowCount=%d", d.Date, d.RowCount)
		}
		if d.Stats.Count != d.RowCount {
			t.Fatalf("daily stats count mismatch")
		}
		// 시간대 검증
		for _, h := range d.Hours {
			switch h.Hour {
			case 2:
				if h.Stats.Count != 0 {
					t.Fatal("hour2는 NULL이어야 함")
				}
			case 15:
				if math.Abs(h.Stats.Avg-34) > 1e-6 || h.Stats.Min != 28 {
					t.Fatalf("override 미반영: %+v", h.Stats)
				}
			default:
				if math.Abs(h.Stats.Avg-30) > 1e-6 {
					t.Fatalf("hour %d avg=%g", h.Hour, h.Stats.Avg)
				}
			}
			if h.DailyCol != model.DailyColSuffix(h.Hour) || h.HourlyID != model.HourlyLabel(h.Hour) {
				t.Fatal("라벨 매핑 오류")
			}
		}
	}
	// 두 날짜의 데이터는 서로 달라야 함(seed 파생)
	if p.Days[0].Stats.MaxTS == p.Days[1].Stats.MaxTS &&
		p.Days[0].Stats.Avg == p.Days[1].Stats.Avg &&
		p.Days[0].Stats.Sum == p.Days[1].Stats.Sum {
		t.Fatal("두 날짜 데이터가 동일 — seed 파생 실패 의심")
	}
}

func TestRowsForMatchesPreview(t *testing.T) {
	s := scenario()
	p, err := Build(s, fixedNow())
	if err != nil {
		t.Fatal(err)
	}
	rows, err := RowsFor(&s, 660070, "2026-08-10")
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(rows)) != p.Days[0].RowCount {
		t.Fatalf("preview와 실행 행수 불일치: %d vs %d", len(rows), p.Days[0].RowCount)
	}
}

func TestRetentionWarning(t *testing.T) {
	s := scenario()
	s.StartDate = "2026-07-01"
	s.EndDate = "2026-07-01"
	p, err := Build(s, fixedNow())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, w := range p.Warnings {
		if len(w) > 0 && contains(w, "보존 창") {
			found = true
		}
	}
	if !found {
		t.Fatal("보존 창 경고 누락")
	}
}

func TestDuplicateOverrideRejected(t *testing.T) {
	s := scenario()
	s.Overrides = append(s.Overrides, model.HourOverride{Hour: 2, Mode: "goal", Goal: s.Daily})
	if _, err := Build(s, fixedNow()); err == nil {
		t.Fatal("중복 override가 통과됨")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
