// Package report는 실행/검증 결과를 Markdown, JSON, CSV로 만든다.
package report

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strings"

	"rawgen/internal/executor"
	"rawgen/internal/model"
	"rawgen/internal/verify"
)

type Bundle struct {
	Preview *model.Preview      `json:"preview,omitempty"`
	Run     *executor.RunResult `json:"run,omitempty"`
	Verify  *verify.Result      `json:"verify,omitempty"`
	Version string              `json:"version"`
}

func Markdown(b Bundle) string {
	var w strings.Builder
	w.WriteString("# rawgen Report\n\n")
	if b.Version != "" {
		fmt.Fprintf(&w, "- tool version: %s\n", b.Version)
	}

	if b.Preview != nil {
		s := b.Preview.Scenario
		w.WriteString("\n## Scenario\n\n")
		fmt.Fprintf(&w, "- checkpoints: %v\n", s.CheckpointIDs)
		fmt.Fprintf(&w, "- date: %s..%s (%s)\n", s.StartDate, s.EndDate, s.Timezone)
		fmt.Fprintf(&w, "- interval: %ds / seed: %d / driver: %s\n", s.IntervalSec, s.Seed, s.DriverCode)
		fmt.Fprintf(&w, "- daily goal: min %g / max %g / avg %g\n", s.Daily.Min, s.Daily.Max, s.Daily.Avg)
		for _, o := range s.Overrides {
			if o.Mode == "null" {
				fmt.Fprintf(&w, "- hour %02d: NULL(생성 안 함)\n", o.Hour)
			} else {
				fmt.Fprintf(&w, "- hour %02d: min %g / max %g / avg %g\n", o.Hour, o.Goal.Min, o.Goal.Max, o.Goal.Avg)
			}
		}
		fmt.Fprintf(&w, "- total rows: %d\n", b.Preview.TotalRows)

		w.WriteString("\n## Expected Daily Stats\n\n")
		w.WriteString("| cp | date | count | min | max | avg | max_time |\n|---|---|---|---|---|---|---|\n")
		for _, d := range b.Preview.Days {
			if d.Stats.Count == 0 {
				fmt.Fprintf(&w, "| %d | %s | 0 | NULL | NULL | NULL | NULL |\n", d.CheckpointID, d.Date)
				continue
			}
			fmt.Fprintf(&w, "| %d | %s | %d | %.6g | %.6g | %.9g | %s |\n",
				d.CheckpointID, d.Date, d.Stats.Count, d.Stats.Min, d.Stats.Max, d.Stats.Avg, d.Stats.MaxTS)
		}

		if len(b.Preview.Warnings) > 0 {
			w.WriteString("\n## Warnings\n\n")
			for _, warn := range b.Preview.Warnings {
				fmt.Fprintf(&w, "- %s\n", warn)
			}
		}
	}

	w.WriteString("\n## Conventions\n\n")
	w.WriteString("- daily_stats(MariaDB): CH `log_hour = H` -> `*_{H+1}` (종료 라벨, `_01`~`_24`)\n")
	w.WriteString("- hourly_checkpoint_data_log: `hour = H` -> H (시작 라벨, daily와 반대)\n")
	w.WriteString("- 빈 시간대: daily는 컬럼 NULL, hourly는 행 미생성\n")
	w.WriteString("- 일 평균: count 가중평균 `sum(avg*cnt)/sum(cnt)`\n")
	fmt.Fprintf(&w, "- tolerance: MariaDB 경유 전 항목 %g, CH↔CH 정확 일치\n", model.MariaTolerance)
	fmt.Fprintf(&w, "- 보존 창: checkvalue 파티션·daily_stats TTL = %d일\n", model.RetentionDays)

	if b.Run != nil {
		w.WriteString("\n## Generate Run\n\n")
		fmt.Fprintf(&w, "- run_id: %s / profile: %s / dry-run: %v\n", b.Run.RunID, b.Run.Profile, b.Run.DryRun)
		fmt.Fprintf(&w, "- started: %s / finished: %s\n", b.Run.StartedAt, b.Run.FinishedAt)
		if b.Run.Canceled {
			w.WriteString("- **취소됨** (batch 경계에서 중단)\n")
		}
		if b.Run.Error != "" {
			fmt.Fprintf(&w, "- **오류**: %s\n", b.Run.Error)
		}
		w.WriteString("\n| cp | date | planned | inserted | readback | note | checksum |\n|---|---|---|---|---|---|---|\n")
		for _, d := range b.Run.Days {
			mark := "FAIL"
			if d.ReadbackOK {
				mark = "OK"
			}
			fmt.Fprintf(&w, "| %d | %s | %d | %d | %s | %s | %s |\n",
				d.CheckpointID, d.Date, d.Planned, d.Inserted, mark, d.ReadbackNote, d.Checksum)
		}
		if !b.Run.DryRun {
			w.WriteString("\n### 오염 마킹\n\n")
			w.WriteString("아래 (날짜, checkpoint)는 이 도구가 인위 데이터를 주입한 범위입니다. **다른 검증의 클린 대조 기준으로 사용하지 마세요.**\n\n")
			for _, d := range b.Run.Days {
				fmt.Fprintf(&w, "- %s / cp %d\n", d.Date, d.CheckpointID)
			}
			w.WriteString("\n생성 범위 식별 쿼리:\n\n```sql\n")
			for _, d := range b.Run.Days {
				fmt.Fprintf(&w, "SELECT count() FROM checkvalue WHERE checkpoint_id = %d AND toDate(log_date) = '%s';\n",
					d.CheckpointID, d.Date)
			}
			w.WriteString("```\n")
		}
	}

	if b.Verify != nil {
		w.WriteString("\n## Verification\n\n")
		fmt.Fprintf(&w, "- 복제 지연 가드: %v (delay %d초)\n", b.Verify.GuardPassed, b.Verify.ReplicationDelay)
		if len(b.Verify.ExcludeDates) > 0 {
			fmt.Fprintf(&w, "- exclude_date 등재(최근): %s\n", strings.Join(b.Verify.ExcludeDates, ", "))
		}
		for _, warn := range b.Verify.Warnings {
			fmt.Fprintf(&w, "- 주의: %s\n", warn)
		}
		w.WriteString("\n| layer | ran | result | checked | skipped(창 밖) | mismatches | note |\n|---|---|---|---|---|---|---|\n")
		for _, lr := range []verify.LayerResult{b.Verify.L1Raw, b.Verify.L1MV, b.Verify.L2Daily, b.Verify.L2Hourly} {
			// 판정은 verify가 정한 3값(+ERROR)을 그대로 쓴다. 리포트가 따로 계산하면
			// 화면과 문서가 갈라지고, 갈라지는 쪽은 늘 "미검증을 통과로" 읽는 쪽이었다.
			status := lr.Verdict()
			switch status {
			case verify.VerdictError:
				status = "ERROR(대조 불가)"
			case verify.VerdictInconclusive:
				status = "INCONCLUSIVE(미검증)"
			}
			note := lr.Note
			if lr.Errored && lr.Err != "" {
				note = lr.Err
			}
			fmt.Fprintf(&w, "| %s | %v | %s | %d | %d | %d | %s |\n", lr.Name, lr.Ran, status, lr.Checked, lr.Skipped, len(lr.Mismatches), note)
		}
		overall := b.Verify.Verdict()
		if overall == verify.VerdictInconclusive {
			overall = "INCONCLUSIVE(미검증 — 대조하지 못한 층이 있음)"
		}
		fmt.Fprintf(&w, "\n**전체 판정: %s**\n", overall)

		// 경로 차이는 결함이 아니라 관측이다. 불일치 표와 섞지 않고 따로 낸다.
		if len(b.Verify.PathDiffs) > 0 {
			w.WriteString("\n### NaN 경로 차이 관측 (결함 아님)\n\n")
			w.WriteString("NaN이 섞인 시간대는 MV 경로(필터 없음)와 checkvalue 폴백 경로(isFinite)가 서로 다른 통계를 만듭니다. ")
			w.WriteString("matched 열이 실측이 어느 경로의 기대와 맞았는지 보여줍니다(none = 두 경로 어느 쪽과도 불일치 = 결함).\n\n")
			w.WriteString("| layer | cp | date | hour | field | NaN행 | finite 기대(폴백) | 전체 기대(MV) | 실측 | matched |\n|---|---|---|---|---|---|---|---|---|---|\n")
			for _, d := range b.Verify.PathDiffs {
				fmt.Fprintf(&w, "| %s | %d | %s | %d | %s | %d | %s | %s | %s | %s |\n",
					d.Layer, d.CP, d.Date, d.Hour, d.Field, d.NaNRows, d.Finite, d.All, d.Actual, d.Matched)
			}
		}

		all := allMismatches(b.Verify)
		if len(all) > 0 {
			w.WriteString("\n### Mismatch Samples\n\n")
			w.WriteString("| layer | cp | date | hour | field | expected | actual | note |\n|---|---|---|---|---|---|---|---|\n")
			for _, m := range all {
				hour := fmt.Sprint(m.Hour)
				if m.Hour < 0 {
					hour = "일별"
				}
				fmt.Fprintf(&w, "| %s | %d | %s | %s | %s | %s | %s | %s |\n",
					m.Layer, m.CP, m.Date, hour, m.Field, m.Expected, m.Actual, m.Note)
			}
		}
	}
	return w.String()
}

func allMismatches(v *verify.Result) []verify.Mismatch {
	var all []verify.Mismatch
	for _, lr := range []verify.LayerResult{v.L1Raw, v.L1MV, v.L2Daily, v.L2Hourly} {
		all = append(all, lr.Mismatches...)
	}
	return all
}

func JSON(b Bundle) (string, error) {
	data, err := json.MarshalIndent(b, "", "  ")
	return string(data), err
}

// CSV는 mismatch 목록을 CSV 문자열로 만든다.
func CSV(v *verify.Result) (string, error) {
	var sb strings.Builder
	w := csv.NewWriter(&sb)
	_ = w.Write([]string{"layer", "cp", "date", "hour", "field", "expected", "actual", "diff", "note"})
	for _, m := range allMismatches(v) {
		_ = w.Write([]string{m.Layer, fmt.Sprint(m.CP), m.Date, fmt.Sprint(m.Hour), m.Field, m.Expected, m.Actual, fmt.Sprint(m.Diff), m.Note})
	}
	w.Flush()
	return sb.String(), w.Error()
}
