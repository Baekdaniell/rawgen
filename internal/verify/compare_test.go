package verify

import (
	"testing"
	"time"

	"oqt325/internal/mariadb"
	"oqt325/internal/model"
)

func day(t *testing.T, hour int, count int64) model.DayExpected {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Seoul")
	if err != nil {
		t.Fatal(err)
	}
	mt := time.Date(2026, 8, 14, hour, 30, 0, 0, loc)
	return model.DayExpected{
		CheckpointID: 100, Date: "2026-08-14",
		Hours: []model.HourExpected{{Date: "2026-08-14", Hour: hour, Stats: model.Stats{
			Count: count, Min: 20, Max: 40, Avg: 30, MaxTime: &mt, MaxTS: mt.Format("2006-01-02 15:04:05"),
		}}},
	}
}

// 제품이 행은 만들되 값을 NULL로 쓰면 결함이다. 건너뛰면 "대조 0건 → 통과"가 된다.
func TestCompareHourlyNullValuesAreMismatch(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Seoul")
	now := time.Date(2026, 8, 14, 12, 30, 0, 0, loc)
	lr := &LayerResult{Pass: true}

	compareHourly(lr, day(t, 11, 60), []mariadb.HourlyRow{{Hour: 11}}, now, loc, 100, nil)

	if lr.Pass {
		t.Fatal("min/max/avg가 전부 NULL인데 통과로 판정했다")
	}
	fields := map[string]bool{}
	for _, m := range lr.Mismatches {
		fields[m.Field] = true
	}
	for _, f := range []string{"min", "max", "avg"} {
		if !fields[f] {
			t.Errorf("%s NULL이 불일치로 잡히지 않았다 (mismatches=%+v)", f, lr.Mismatches)
		}
	}
}

// max_time을 해석하지 못하면(NULL·DATETIME 파싱 실패) 대조가 사라지는 대신 불일치로 잡는다.
func TestCompareHourlyUnparsedMaxTimeIsMismatch(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Seoul")
	now := time.Date(2026, 8, 14, 12, 30, 0, 0, loc)
	lr := &LayerResult{Pass: true}
	v := 20.0
	a := 30.0
	m := 40.0

	compareHourly(lr, day(t, 11, 60),
		[]mariadb.HourlyRow{{Hour: 11, Min: &v, Max: &m, Avg: &a, MaxTimeRaw: "0000-00-00 00:00:00"}}, now, loc, 100, nil)

	found := false
	for _, mm := range lr.Mismatches {
		if mm.Field == "max_time" {
			found = true
			if mm.Actual == "" {
				t.Error("실측값 원문이 비어 있다")
			}
		}
	}
	if !found {
		t.Errorf("max_time 미해석이 불일치로 잡히지 않았다 (mismatches=%+v)", lr.Mismatches)
	}
}

// 정상 값이면 통과해야 한다(거짓 실패 방지).
func TestCompareHourlyHappyPath(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Seoul")
	now := time.Date(2026, 8, 14, 12, 30, 0, 0, loc)
	lr := &LayerResult{Pass: true}
	de := day(t, 11, 60)
	mn, mx, av := 20.0, 40.0, 30.0
	ms := de.Hours[0].Stats.MaxTime.UnixMilli()

	compareHourly(lr, de, []mariadb.HourlyRow{{Hour: 11, Min: &mn, Max: &mx, Avg: &av, MaxTimeMs: &ms}}, now, loc, 100, nil)

	if !lr.Pass {
		t.Errorf("정상 행인데 불일치가 났다: %+v", lr.Mismatches)
	}
}

func TestLayerResultFailMarksErrored(t *testing.T) {
	lr := &LayerResult{Pass: true}
	lr.fail("hourly 조회 실패: ", errStub{})
	if lr.Pass || !lr.Errored || lr.Err == "" {
		t.Errorf("조회 실패가 층 결과에 남지 않았다: %+v", lr)
	}
}

type errStub struct{}

func (errStub) Error() string { return "boom" }

// 보존 창 만료 시각: 당일 행은 익일 00:05, 23시 행은 하루 더.
func TestHourlyWindowEnd(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Seoul")
	got := HourlyWindowEnd("2026-08-14", 10, loc)
	want := time.Date(2026, 8, 15, 0, 5, 0, 0, loc)
	if !got.Equal(want) {
		t.Errorf("hour 10 만료 = %v, want %v", got, want)
	}
	got23 := HourlyWindowEnd("2026-08-14", 23, loc)
	want23 := time.Date(2026, 8, 16, 0, 5, 0, 0, loc)
	if !got23.Equal(want23) {
		t.Errorf("hour 23 만료 = %v, want %v", got23, want23)
	}
}
