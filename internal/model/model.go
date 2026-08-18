// Package model은 코어 전반이 공유하는 핵심 자료형을 정의한다.
package model

import (
	"errors"
	"fmt"
	"time"
)

// 보존 창: checkvalue 일 파티션 = daily_stats(CH) TTL = 14일.
// 이보다 과거 날짜는 실환경에서 주입/검증이 불가하다.
const RetentionDays = 14

// MariaDB 경유 대조 허용오차(전 항목 공통). CH↔CH는 정확 일치.
const MariaTolerance = 1e-6

type Goal struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
	Avg float64 `json:"avg"`
}

type HourOverride struct {
	Hour int    `json:"hour"`
	Mode string `json:"mode"` // "goal" | "null"
	Goal Goal   `json:"goal"`
	// NaNCount는 그 시간대 행 중 NaN으로 만들 행 수다(0=없음).
	// 제품의 두 통계 경로가 NaN을 다르게 취급하는지 실증하기 위한 것이다:
	// MV 경로는 NaN도 세고 평균에 넣지만, checkvalue 폴백 경로는 isFinite로 걸러낸다.
	// 나머지 행은 그대로 goal(min/max/avg)을 만족한다.
	NaNCount int `json:"nanCount,omitempty"`
}

type Scenario struct {
	Name          string         `json:"name"`
	CheckpointIDs []int64        `json:"checkpointIds"`
	StartDate     string         `json:"startDate"` // YYYY-MM-DD
	EndDate       string         `json:"endDate"`
	Timezone      string         `json:"timezone"`
	IntervalSec   int            `json:"intervalSec"`
	Daily         Goal           `json:"daily"`
	Overrides     []HourOverride `json:"hourlyOverrides"`
	Seed          int64          `json:"seed"`
	BatchSize     int            `json:"batchSize"`
	DriverCode    string         `json:"driverCode"` // numeric_raw | numeric_percent
}

func (s *Scenario) Location() (*time.Location, error) {
	if s.Timezone == "" {
		return nil, errors.New("timezone이 필요합니다")
	}
	return time.LoadLocation(s.Timezone)
}

// Validate는 시나리오 정합성을 검사한다. now는 보존 창 판정 기준 시각.
func (s *Scenario) Validate(now time.Time) ([]string, error) {
	var warnings []string
	if len(s.CheckpointIDs) == 0 {
		return nil, errors.New("checkpoint id가 최소 1개 필요합니다")
	}
	if s.IntervalSec <= 0 || s.IntervalSec > 3600 {
		return nil, errors.New("수집 주기는 1~3600초 범위여야 합니다")
	}
	if s.DriverCode != "numeric_raw" && s.DriverCode != "numeric_percent" {
		return nil, fmt.Errorf("driver_code %q는 지원하지 않습니다 (numeric_raw | numeric_percent)", s.DriverCode)
	}
	loc, err := s.Location()
	if err != nil {
		return nil, fmt.Errorf("timezone 오류: %w", err)
	}
	start, err := time.ParseInLocation("2006-01-02", s.StartDate, loc)
	if err != nil {
		return nil, fmt.Errorf("시작 날짜 형식 오류: %w", err)
	}
	end, err := time.ParseInLocation("2006-01-02", s.EndDate, loc)
	if err != nil {
		return nil, fmt.Errorf("종료 날짜 형식 오류: %w", err)
	}
	if end.Before(start) {
		return nil, errors.New("종료 날짜는 시작 날짜보다 빠를 수 없습니다")
	}
	if end.Sub(start) > 31*24*time.Hour {
		return nil, errors.New("날짜 범위는 최대 31일입니다")
	}

	seen := map[int]bool{}
	for _, o := range s.Overrides {
		if o.Hour < 0 || o.Hour > 23 {
			return nil, fmt.Errorf("override hour %d는 0~23 범위여야 합니다", o.Hour)
		}
		if seen[o.Hour] {
			return nil, fmt.Errorf("hour %d override가 중복입니다", o.Hour)
		}
		seen[o.Hour] = true
		if o.Mode != "goal" && o.Mode != "null" {
			return nil, fmt.Errorf("override mode %q는 goal 또는 null이어야 합니다", o.Mode)
		}
		if o.NaNCount < 0 {
			return nil, fmt.Errorf("hour %d의 NaN 행 수는 음수일 수 없습니다", o.Hour)
		}
		if o.NaNCount > 0 && o.Mode == "null" {
			return nil, fmt.Errorf("hour %d: 생성 안 함(null) 시간대에는 NaN을 넣을 수 없습니다", o.Hour)
		}
		if o.NaNCount > 0 {
			total := 3600 / s.IntervalSec
			if o.NaNCount >= total {
				return nil, fmt.Errorf("hour %d: NaN %d행은 그 시간대 전체 행 수(%d)보다 적어야 합니다 — 정상 행이 남아야 goal을 만족시킬 수 있습니다", o.Hour, o.NaNCount, total)
			}
			warnings = append(warnings, fmt.Sprintf(
				"hour %d에 NaN %d행이 섞입니다 — MV 경로와 checkvalue 폴백 경로의 통계가 서로 달라집니다(의도된 경로 차이 검출)", o.Hour, o.NaNCount))
		}
	}

	// 보존 창(14일): today-13일 이전 날짜는 실환경 검증 불가
	limit := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -(RetentionDays - 1))
	if start.Before(limit) {
		warnings = append(warnings, fmt.Sprintf(
			"시작 날짜가 보존 창(%d일) 밖입니다. checkvalue 파티션·daily_stats TTL 만료로 실환경 검증이 불가합니다 (%s 이후만 유효)",
			RetentionDays, limit.Format("2006-01-02")))
	}
	if !end.Before(time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)) {
		warnings = append(warnings, "종료 날짜가 오늘 이후입니다. 당일 데이터는 제품 통계 생성(D+1)과 겹쳐 검증 결과가 왜곡될 수 있습니다")
	}
	return warnings, nil
}

// NaNCountForHour는 그 시간대에 섞을 NaN 행 수다.
func (s *Scenario) NaNCountForHour(hour int) int {
	for _, o := range s.Overrides {
		if o.Hour == hour {
			return o.NaNCount
		}
	}
	return 0
}

// HasNaN은 시나리오에 NaN 주입이 포함되는지 알려준다(주입 게이트에서 쓴다).
func (s *Scenario) HasNaN() bool {
	for _, o := range s.Overrides {
		if o.NaNCount > 0 {
			return true
		}
	}
	return false
}

// GoalForHour는 override를 반영한 해당 시간대 목표를 반환한다. nil이면 생성 안 함(NULL).
func (s *Scenario) GoalForHour(hour int) *Goal {
	for _, o := range s.Overrides {
		if o.Hour == hour {
			if o.Mode == "null" {
				return nil
			}
			g := o.Goal
			return &g
		}
	}
	g := s.Daily
	return &g
}

// Row는 checkvalue 한 행이다.
type Row struct {
	LogDate      time.Time `json:"-"`
	LogDateText  string    `json:"log_date"`
	CheckpointID int64     `json:"checkpoint_id"`
	RawData      float64   `json:"raw_data"`
	Data         string    `json:"data"`
}

// Stats는 구간 통계(기대값 또는 실측값)다. Count==0이면 나머지는 무의미(NULL).
type Stats struct {
	Count   int64      `json:"count"`
	Min     float64    `json:"min"`
	Max     float64    `json:"max"`
	Avg     float64    `json:"avg"`
	Sum     float64    `json:"sum"`
	MaxTime *time.Time `json:"-"`
	MaxTS   string     `json:"maxTime,omitempty"` // 표시용
	// NaNCount는 이 구간에 섞인 NaN 행 수다. Count는 이 Stats의 기준에 따라
	// NaN을 포함하거나(전체 기준) 제외한다(finite 기준).
	NaNCount int64 `json:"nanCount,omitempty"`
}

// HourExpected는 시간대 기대값이다. NaN이 섞이면 제품 경로에 따라 정답이 갈리므로
// 기대값을 두 벌로 들고 있는다.
//   - Stats:    finite 기준 = checkvalue 폴백 경로(isFinite 필터)가 만들어야 할 값
//   - StatsAll: 전체 기준   = MV 경로(필터 없음)가 만들어야 할 값(avg가 NaN이 될 수 있음)
//
// NaN이 없으면 두 값은 동일하다.
type HourExpected struct {
	Date     string `json:"date"`
	Hour     int    `json:"hour"`
	DailyCol string `json:"dailyCol"`  // daily_stats 컬럼 접미사: _{H+1} (종료 라벨)
	HourlyID string `json:"hourlyCol"` // hourly_checkpoint_data_log hour 값: H (시작 라벨)
	Stats    Stats  `json:"stats"`
	StatsAll Stats  `json:"statsAll"`
}

// HasNaN은 이 시간대에 NaN 행이 있는지 알려준다.
func (h HourExpected) HasNaN() bool { return h.StatsAll.NaNCount > 0 }

type DayExpected struct {
	CheckpointID int64          `json:"checkpointId"`
	Date         string         `json:"date"`
	Stats        Stats          `json:"stats"`
	StatsAll     Stats          `json:"statsAll"`
	Hours        []HourExpected `json:"hours"`
	RowCount     int64          `json:"rowCount"`
}

// HasNaN은 이 날짜에 NaN 행이 있는지 알려준다.
func (d DayExpected) HasNaN() bool { return d.StatsAll.NaNCount > 0 }

// Preview는 dry-run 결과다.
type Preview struct {
	Scenario   Scenario      `json:"scenario"`
	Days       []DayExpected `json:"days"` // (checkpoint × 날짜) 별
	TotalRows  int64         `json:"totalRows"`
	Warnings   []string      `json:"warnings"`
	SampleRows []Row         `json:"sampleRows"`
}

func DailyColSuffix(hour int) string {
	return fmt.Sprintf("_%02d", hour+1)
}

func HourlyLabel(hour int) string {
	return fmt.Sprintf("%02d", hour)
}
