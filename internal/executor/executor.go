// Package executor는 생성 데이터를 ClickHouse에 batch INSERT하고,
// 직후 readback(건수 대조 + 중복 카나리)으로 실제 적재를 확인한다.
// readback은 옵션이 아니라 고정 단계다 — insert_deduplicate가 재실행 INSERT를
// 조용히 무시하는 함정과, 도구 자신이 만드는 중복 오염 양쪽을 여기서 잡는다(계획서 §7.2).
package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"time"

	"rawgen/internal/chdb"
	"rawgen/internal/model"
	"rawgen/internal/planner"
	"rawgen/internal/profile"
)

type DayResult struct {
	CheckpointID int64  `json:"checkpointId"`
	Date         string `json:"date"`
	Planned      int64  `json:"planned"`
	Inserted     int64  `json:"inserted"`
	ReadbackOK   bool   `json:"readbackOk"`
	ReadbackNote string `json:"readbackNote"`
	Duplicates   int64  `json:"duplicates"`
	Checksum     string `json:"checksum"`
}

type RunResult struct {
	RunID      string      `json:"runId"`
	Profile    string      `json:"profile"`
	StartedAt  string      `json:"startedAt"`
	FinishedAt string      `json:"finishedAt"`
	DryRun     bool        `json:"dryRun"`
	Days       []DayResult `json:"days"`
	TotalRows  int64       `json:"totalRows"`
	Canceled   bool        `json:"canceled"`
	Error      string      `json:"error,omitempty"`
	Scenario   model.Scenario `json:"scenario"`
}

type Progress struct {
	Phase   string `json:"phase"` // generate | insert | readback
	CP      int64  `json:"cp"`
	Date    string `json:"date"`
	Done    int64  `json:"done"`
	Total   int64  `json:"total"`
	Message string `json:"message"`
}

// Run은 시나리오 전체를 실행한다. dryRun=true면 DB에 쓰지 않는다.
func Run(ctx context.Context, p profile.Profile, s model.Scenario, dryRun bool, emit func(Progress)) (*RunResult, error) {
	res := &RunResult{
		RunID:     fmt.Sprintf("run-%s", time.Now().Format("20060102-150405")),
		Profile:   p.Name,
		StartedAt: time.Now().Format(time.RFC3339),
		DryRun:    dryRun,
		Scenario:  s,
	}

	// 안전장치: TestOnly 프로파일에서만 실제 INSERT 허용
	if !dryRun && !p.TestOnly {
		return res, fmt.Errorf("세션 %q은 TestOnly가 아닙니다. 테스트 DB로 확인된 세션에서만 INSERT할 수 있습니다", p.Name)
	}

	// 보존 창 밖 날짜는 실행 차단 (preview는 경고, 실행은 차단)
	warnings, err := s.Validate(time.Now())
	if err != nil {
		return res, err
	}
	if !dryRun {
		for _, w := range warnings {
			if len(w) > 0 && (contains(w, "보존 창")) {
				return res, fmt.Errorf("실행 차단: %s", w)
			}
		}
	}

	preview, err := planner.Build(s, time.Now())
	if err != nil {
		return res, err
	}

	var ch *chdb.Client
	if !dryRun {
		ch, err = chdb.Open(p)
		if err != nil {
			return res, fmt.Errorf("ClickHouse 연결 실패: %w", err)
		}
		defer ch.Close()
	}

	loc, _ := s.Location()
	for _, de := range preview.Days {
		if ctx.Err() != nil {
			res.Canceled = true
			break
		}
		dr := DayResult{CheckpointID: de.CheckpointID, Date: de.Date, Planned: de.RowCount}
		rows, err := planner.RowsFor(&s, de.CheckpointID, de.Date)
		if err != nil {
			res.Error = err.Error()
			res.Days = append(res.Days, dr)
			break
		}
		dr.Checksum = checksum(rows)
		emit(Progress{Phase: "generate", CP: de.CheckpointID, Date: de.Date, Done: 0, Total: de.RowCount,
			Message: fmt.Sprintf("cp %d %s: %d행 생성됨", de.CheckpointID, de.Date, len(rows))})

		if dryRun {
			dr.ReadbackOK = true
			dr.ReadbackNote = "dry-run: DB 미접근"
			res.Days = append(res.Days, dr)
			res.TotalRows += dr.Planned
			continue
		}

		ins, err := ch.InsertRows(ctx, rows, s.BatchSize, func(done int64) {
			emit(Progress{Phase: "insert", CP: de.CheckpointID, Date: de.Date, Done: done, Total: de.RowCount})
		})
		if ins != nil {
			dr.Inserted = ins.Inserted
		}
		if err != nil {
			if ctx.Err() != nil {
				res.Canceled = true
			}
			res.Error = err.Error()
			res.Days = append(res.Days, dr)
			break
		}

		// readback: 건수 + 중복 카나리
		day, _ := time.ParseInLocation("2006-01-02", de.Date, loc)
		from, to := day, day.AddDate(0, 0, 1)
		count, err := ch.CountRange(ctx, de.CheckpointID, from, to)
		if err != nil {
			dr.ReadbackNote = "readback 실패: " + err.Error()
		} else {
			dup, derr := ch.DuplicateRows(ctx, de.CheckpointID, from, to)
			if derr == nil {
				dr.Duplicates = dup
			}
			switch {
			case count < dr.Planned:
				dr.ReadbackNote = fmt.Sprintf("적재 %d < 계획 %d — insert_deduplicate가 동일 블록을 무시했을 가능성(같은 시나리오 재실행?). seed를 바꾸거나 기존 범위를 확인하세요", count, dr.Planned)
			case dup > 0:
				dr.ReadbackNote = fmt.Sprintf("완전 동일 행 %d건 검출 — 이 범위에 중복 적재가 존재합니다(재실행 또는 기존 데이터와 충돌)", dup)
			case count > dr.Planned:
				dr.ReadbackOK = true
				dr.ReadbackNote = fmt.Sprintf("적재 확인(범위 내 총 %d행 — 기존 데이터 %d행 포함)", count, count-dr.Planned)
			default:
				dr.ReadbackOK = true
				dr.ReadbackNote = fmt.Sprintf("적재 확인(%d행, 중복 0)", count)
			}
		}
		emit(Progress{Phase: "readback", CP: de.CheckpointID, Date: de.Date, Done: dr.Inserted, Total: de.RowCount, Message: dr.ReadbackNote})
		res.Days = append(res.Days, dr)
		res.TotalRows += dr.Inserted
	}

	res.FinishedAt = time.Now().Format(time.RFC3339)
	_ = appendHistory(res)
	return res, nil
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

func checksum(rows []model.Row) string {
	h := fnv.New64a()
	for i := range rows {
		fmt.Fprintf(h, "%s|%d|%g\n", rows[i].LogDateText, rows[i].CheckpointID, rows[i].RawData)
	}
	return fmt.Sprintf("%016x", h.Sum64())
}

// ---------- 실행 이력 (secret 미포함) ----------

func historyPath() (string, error) {
	dir, err := profile.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "history.jsonl"), nil
}

func appendHistory(r *RunResult) error {
	path, err := historyPath()
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = f.Write(append(data, '\n'))
	return err
}

// History는 최근 실행 이력을 최신순으로 반환한다.
func History(limit int) ([]RunResult, error) {
	path, err := historyPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return []RunResult{}, nil
	}
	if err != nil {
		return nil, err
	}
	var all []RunResult
	for _, line := range splitLines(data) {
		if len(line) == 0 {
			continue
		}
		var r RunResult
		if json.Unmarshal(line, &r) == nil {
			all = append(all, r)
		}
	}
	// 최신순
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

func splitLines(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			out = append(out, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		out = append(out, data[start:])
	}
	return out
}
