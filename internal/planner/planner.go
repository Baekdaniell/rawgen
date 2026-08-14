// Package planner는 시나리오로부터 preview(기대 통계, 예상 row 수, 경고)를 만든다.
package planner

import (
	"fmt"
	"math"
	"time"

	"oqt325/internal/generator"
	"oqt325/internal/model"
)

const sampleLimit = 12

// Build는 dry-run preview를 생성한다. 실제 DB 접근 없음.
func Build(s model.Scenario, now time.Time) (*model.Preview, error) {
	warnings, err := s.Validate(now)
	if err != nil {
		return nil, err
	}
	loc, err := s.Location()
	if err != nil {
		return nil, err
	}
	start, _ := time.ParseInLocation("2006-01-02", s.StartDate, loc)
	end, _ := time.ParseInLocation("2006-01-02", s.EndDate, loc)

	p := &model.Preview{Scenario: s, Warnings: warnings}
	for _, cp := range s.CheckpointIDs {
		for day := start; !day.After(end); day = day.AddDate(0, 0, 1) {
			dateText := day.Format("2006-01-02")
			var dayRows []model.Row
			de := model.DayExpected{CheckpointID: cp, Date: dateText}
			for hour := 0; hour < 24; hour++ {
				rows, residual, err := generator.HourRows(&s, cp, day, hour, loc)
				if err != nil {
					return nil, err
				}
				if goal := s.GoalForHour(hour); goal != nil {
					tol := 1e-9 * math.Max(1, math.Abs(goal.Avg)) * float64(3600/s.IntervalSec)
					if math.Abs(residual) > tol {
						p.Warnings = append(p.Warnings, fmt.Sprintf(
							"cp %d %s hour %d: 목표 avg %g 수렴 실패(잔차 %.3e). 기대값은 실제 생성값 기준으로 계산됩니다",
							cp, dateText, hour, goal.Avg, residual))
					}
				}
				de.Hours = append(de.Hours, model.HourExpected{
					Date:     dateText,
					Hour:     hour,
					DailyCol: model.DailyColSuffix(hour),
					HourlyID: model.HourlyLabel(hour),
					Stats:    generator.StatsFor(rows),
				})
				dayRows = append(dayRows, rows...)
			}
			de.Stats = generator.StatsFor(dayRows)
			de.RowCount = int64(len(dayRows))
			p.Days = append(p.Days, de)
			p.TotalRows += de.RowCount
			if len(p.SampleRows) < sampleLimit {
				n := sampleLimit - len(p.SampleRows)
				if n > len(dayRows) {
					n = len(dayRows)
				}
				p.SampleRows = append(p.SampleRows, dayRows[:n]...)
			}
		}
	}
	return p, nil
}

// RowsFor는 실행 시 (cp, 날짜) 단위 실제 행을 재생성한다(preview와 동일 seed → 동일 데이터).
func RowsFor(s *model.Scenario, cp int64, dateText string) ([]model.Row, error) {
	loc, err := s.Location()
	if err != nil {
		return nil, err
	}
	day, err := time.ParseInLocation("2006-01-02", dateText, loc)
	if err != nil {
		return nil, err
	}
	var all []model.Row
	for hour := 0; hour < 24; hour++ {
		rows, _, err := generator.HourRows(s, cp, day, hour, loc)
		if err != nil {
			return nil, err
		}
		all = append(all, rows...)
	}
	return all, nil
}
