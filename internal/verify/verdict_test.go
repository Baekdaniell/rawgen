package verify

import (
	"testing"
	"time"

	"rawgen/internal/mariadb"
)

// "오늘 주입 → 내일 아침 검증" 절차에서 hourly는 전부 보존 창 밖이라 대조가 0건이 된다.
// 그 상태의 "불일치 0"을 PASS로 읽으면 아무것도 안 본 실행이 통과로 남는다.
func TestOutOfWindowOnlyIsInconclusiveNotPass(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Seoul")
	// 2026-08-16 09:00 시점에 8/14 11시 셀을 본다 = 창 밖(당일+전일 23시만 잔존)
	now := time.Date(2026, 8, 16, 9, 0, 0, 0, loc)
	lr := &LayerResult{Name: "L2 hourly", Ran: true, Pass: true}

	compareHourly(lr, day(t, 11, 60), nil, now, loc, 100, nil)

	if lr.Checked != 0 {
		t.Fatalf("창 밖 시간대를 대조했다: checked=%d", lr.Checked)
	}
	if lr.Skipped != 1 {
		t.Fatalf("대조에서 제외한 시간대 수를 세지 않았다: skipped=%d", lr.Skipped)
	}
	if got := lr.Verdict(); got != VerdictInconclusive {
		t.Fatalf("대조 0건인데 판정이 %s다 (미검증이어야 한다)", got)
	}
}

// 일부만 창 밖이면 본 만큼은 판정한다. 다만 몇 개를 못 봤는지는 남아야 한다.
func TestPartialWindowKeepsPassButCountsSkips(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Seoul")
	now := time.Date(2026, 8, 14, 12, 30, 0, 0, loc)
	lr := &LayerResult{Name: "L2 hourly", Ran: true, Pass: true}

	de := day(t, 11, 60)
	// 아직 생성 시점(13:05)이 오지 않은 12시 시간대를 덧붙인다 = 지금은 대조 불가.
	later := day(t, 12, 60)
	de.Hours = append(de.Hours, later.Hours[0])

	minV, maxV, avgV := 20.0, 40.0, 30.0
	ms := time.Date(2026, 8, 14, 11, 30, 0, 0, loc).UnixMilli()
	compareHourly(lr, de, []mariadb.HourlyRow{{Hour: 11, Min: &minV, Max: &maxV, Avg: &avgV, MaxTimeMs: &ms}}, now, loc, 100, nil)

	if lr.Checked == 0 {
		t.Fatal("창 안 시간대를 대조하지 않았다")
	}
	if lr.Skipped == 0 {
		t.Fatal("창 밖 시간대를 세지 않았다")
	}
	if got := lr.Verdict(); got != VerdictPass {
		t.Fatalf("대조한 항목이 전부 일치하는데 판정이 %s다: %+v", got, lr.Mismatches)
	}
}

// 조회 실패(ERROR)와 미검증(INCONCLUSIVE)과 불일치(FAIL)는 서로 다른 글자여야 한다.
func TestVerdictValues(t *testing.T) {
	cases := []struct {
		name string
		lr   LayerResult
		want string
	}{
		{"미실행", LayerResult{}, VerdictNotRun},
		{"조회 실패", LayerResult{Ran: true, Errored: true}, VerdictError},
		{"불일치", LayerResult{Ran: true, Checked: 3, Mismatches: []Mismatch{{}}}, VerdictFail},
		{"대조 0건", LayerResult{Ran: true, Pass: true}, VerdictInconclusive},
		{"통과", LayerResult{Ran: true, Pass: true, Checked: 3}, VerdictPass},
	}
	for _, c := range cases {
		if got := c.lr.Verdict(); got != c.want {
			t.Errorf("%s: %s (want %s)", c.name, got, c.want)
		}
	}
}

// 실행 전체 판정도 3값이다. 미검증을 FAIL로 뭉뚱그리면 "제품이 틀렸다"로 읽힌다.
func TestResultVerdict(t *testing.T) {
	inconclusive := &Result{Inconclusive: true, L1Raw: LayerResult{Ran: true, Pass: true, Checked: 1}}
	if got := inconclusive.Verdict(); got != VerdictInconclusive {
		t.Fatalf("미검증 실행의 전체 판정이 %s다", got)
	}
	failed := &Result{Inconclusive: true, L2Hourly: LayerResult{Ran: true, Checked: 2, Mismatches: []Mismatch{{}}}}
	if got := failed.Verdict(); got != VerdictFail {
		t.Fatalf("불일치가 있는데 전체 판정이 %s다", got)
	}
}
