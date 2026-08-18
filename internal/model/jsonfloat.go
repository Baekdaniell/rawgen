package model

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
)

// encoding/json은 NaN·Inf를 마샬링하지 못하고 에러를 낸다.
// NaN 주입 시나리오에서는 기대값(전체 기준 avg 등)이 정상적으로 NaN이 되므로,
// 그대로 두면 Preview 응답 전체가 실패해 화면이 비어버린다.
// 숫자는 숫자로, 비유한값은 "NaN"/"Inf"/"-Inf" 문자열로 내보낸다.
type jsonFloat float64

func (f jsonFloat) MarshalJSON() ([]byte, error) {
	v := float64(f)
	switch {
	case math.IsNaN(v):
		return []byte(`"NaN"`), nil
	case math.IsInf(v, 1):
		return []byte(`"Inf"`), nil
	case math.IsInf(v, -1):
		return []byte(`"-Inf"`), nil
	}
	return json.Marshal(v)
}

func (f *jsonFloat) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	switch s {
	case "NaN":
		*f = jsonFloat(math.NaN())
		return nil
	case "Inf", "+Inf":
		*f = jsonFloat(math.Inf(1))
		return nil
	case "-Inf":
		*f = jsonFloat(math.Inf(-1))
		return nil
	case "null", "":
		*f = 0
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return errors.New("숫자로 해석할 수 없습니다: " + s)
	}
	*f = jsonFloat(v)
	return nil
}

// statsJSON은 Stats의 직렬화 표현이다(필드 순서·이름은 기존과 동일).
type statsJSON struct {
	Count    int64     `json:"count"`
	Min      jsonFloat `json:"min"`
	Max      jsonFloat `json:"max"`
	Avg      jsonFloat `json:"avg"`
	Sum      jsonFloat `json:"sum"`
	MaxTS    string    `json:"maxTime,omitempty"`
	NaNCount int64     `json:"nanCount,omitempty"`
}

func (s Stats) MarshalJSON() ([]byte, error) {
	return json.Marshal(statsJSON{
		Count: s.Count, Min: jsonFloat(s.Min), Max: jsonFloat(s.Max),
		Avg: jsonFloat(s.Avg), Sum: jsonFloat(s.Sum), MaxTS: s.MaxTS, NaNCount: s.NaNCount,
	})
}

func (s *Stats) UnmarshalJSON(b []byte) error {
	var j statsJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return err
	}
	s.Count, s.MaxTS, s.NaNCount = j.Count, j.MaxTS, j.NaNCount
	s.Min, s.Max = float64(j.Min), float64(j.Max)
	s.Avg, s.Sum = float64(j.Avg), float64(j.Sum)
	return nil
}

// rowJSON은 Row의 직렬화 표현이다. raw_data도 NaN이 될 수 있다(NaN 주입 샘플 행).
type rowJSON struct {
	LogDateText  string    `json:"log_date"`
	CheckpointID int64     `json:"checkpoint_id"`
	RawData      jsonFloat `json:"raw_data"`
	Data         string    `json:"data"`
}

func (r Row) MarshalJSON() ([]byte, error) {
	return json.Marshal(rowJSON{
		LogDateText: r.LogDateText, CheckpointID: r.CheckpointID,
		RawData: jsonFloat(r.RawData), Data: r.Data,
	})
}

func (r *Row) UnmarshalJSON(b []byte) error {
	var j rowJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return err
	}
	r.LogDateText, r.CheckpointID, r.Data = j.LogDateText, j.CheckpointID, j.Data
	r.RawData = float64(j.RawData)
	return nil
}
