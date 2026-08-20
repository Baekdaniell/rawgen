// Package chdb는 ClickHouse 접근을 담당한다: 스키마 확인, batch INSERT + readback,
// 복제 지연 가드, 카나리(중복 검출), 원본 재집계, MV 조회.
package chdb

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"rawgen/internal/model"
	"rawgen/internal/profile"
)

var identRe = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

func ident(name string) (string, error) {
	if !identRe.MatchString(name) {
		return "", fmt.Errorf("허용되지 않는 식별자: %q", name)
	}
	return name, nil
}

type Client struct {
	conn driver.Conn
	p    profile.Profile
	db   string
}

func Open(p profile.Profile) (*Client, error) {
	p.ApplyDefaults()
	db, err := ident(p.CH.Database)
	if err != nil {
		return nil, err
	}
	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr: []string{fmt.Sprintf("%s:%d", p.CH.Host, p.CH.Port)},
		Auth: clickhouse.Auth{Database: p.CH.Database, Username: p.CH.User, Password: p.CH.Password},
		Settings: clickhouse.Settings{
			"max_execution_time": 300,
		},
		DialTimeout: 8 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn, p: p, db: db}, nil
}

func (c *Client) Close() error { return c.conn.Close() }

func (c *Client) Ping(ctx context.Context) error { return c.conn.Ping(ctx) }

// ---------- 스키마 확인 ----------

type ColumnInfo struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type Discovery struct {
	CheckvalueColumns []ColumnInfo `json:"checkvalueColumns"`
	CheckvalueEngine  string       `json:"checkvalueEngine"`
	Replicated        bool         `json:"replicated"`
	DateTimePrecision string       `json:"dateTimePrecision"`
	MVColumns         []ColumnInfo `json:"mvColumns"`
	MVHasMaxTime      bool         `json:"mvHasMaxTime"`
	ExcludeDates      []string     `json:"excludeDates"`
	Problems          []string     `json:"problems"`
	// 복제 지연(초). verify의 60초 가드에 걸릴지 사전 확인용. -1 = 측정 불가.
	ReplicationDelay int64 `json:"replicationDelay"`
}

func (c *Client) Discover(ctx context.Context) (*Discovery, error) {
	d := &Discovery{}
	cv, err := ident(c.p.CheckvalueTable)
	if err != nil {
		return nil, err
	}
	mv, err := ident(c.p.DailyStatsCHTable)
	if err != nil {
		return nil, err
	}

	cols, engine, err := c.tableColumns(ctx, cv)
	if err != nil {
		return nil, fmt.Errorf("%s 스키마 조회 실패: %w", cv, err)
	}
	d.CheckvalueColumns = cols
	d.CheckvalueEngine = engine
	d.Replicated = strings.Contains(engine, "Replicated")
	need := map[string]bool{"log_date": false, "checkpoint_id": false, "raw_data": false, "data": false}
	for _, col := range cols {
		if _, ok := need[col.Name]; ok {
			need[col.Name] = true
		}
		if col.Name == "log_date" {
			d.DateTimePrecision = col.Type
		}
	}
	for name, ok := range need {
		if !ok {
			d.Problems = append(d.Problems, fmt.Sprintf("%s에 %s 컬럼이 없습니다", cv, name))
		}
	}

	mvCols, _, err := c.tableColumns(ctx, mv)
	if err != nil {
		d.Problems = append(d.Problems, fmt.Sprintf("%s(MV) 스키마 조회 실패: %v", mv, err))
	} else {
		d.MVColumns = mvCols
		mvNeed := map[string]bool{"log_date": false, "log_hour": false, "checkpoint_id": false,
			"min_value": false, "max_value": false, "avg_value": false, "count_value": false}
		for _, col := range mvCols {
			if _, ok := mvNeed[col.Name]; ok {
				mvNeed[col.Name] = true
			}
			if col.Name == "max_time" {
				d.MVHasMaxTime = true
			}
		}
		for name, ok := range mvNeed {
			if !ok {
				d.Problems = append(d.Problems, fmt.Sprintf("%s(MV)에 %s 컬럼이 없습니다 — 세션의 테이블명 오버라이드 확인 필요", mv, name))
			}
		}
	}

	// exclude_date: hourly 폴백 분기 원인 — 검증 판정 전에 반드시 참조(계획서 §7.1)
	if ex, err := ident(c.p.ExcludeDateTable); err == nil {
		rows, err := c.conn.Query(ctx, fmt.Sprintf("SELECT DISTINCT toString(log_date) FROM %s.%s ORDER BY 1 DESC LIMIT 30", c.db, ex))
		if err == nil {
			for rows.Next() {
				var s string
				if rows.Scan(&s) == nil {
					d.ExcludeDates = append(d.ExcludeDates, s)
				}
			}
			rows.Close()
		}
	}
	return d, nil
}

func (c *Client) tableColumns(ctx context.Context, table string) ([]ColumnInfo, string, error) {
	rows, err := c.conn.Query(ctx,
		"SELECT name, type FROM system.columns WHERE database = ? AND table = ? ORDER BY position", c.p.CH.Database, table)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var cols []ColumnInfo
	for rows.Next() {
		var ci ColumnInfo
		if err := rows.Scan(&ci.Name, &ci.Type); err != nil {
			return nil, "", err
		}
		cols = append(cols, ci)
	}
	if len(cols) == 0 {
		return nil, "", fmt.Errorf("테이블 %s.%s이(가) 없습니다", c.p.CH.Database, table)
	}
	var engine string
	row := c.conn.QueryRow(ctx, "SELECT engine FROM system.tables WHERE database = ? AND name = ?", c.p.CH.Database, table)
	_ = row.Scan(&engine)
	return cols, engine, nil
}

// ---------- 복제 지연 가드 ----------

// ReplicationDelay는 해당 DB의 최대 absolute_delay(초)를 반환한다.
// 60초 초과 시 대조 금지 — 지연 복제본 대조는 대량 가짜 불일치를 만든다(실측).
func (c *Client) ReplicationDelay(ctx context.Context) (uint64, error) {
	row := c.conn.QueryRow(ctx,
		"SELECT coalesce(max(absolute_delay), 0) FROM system.replicas WHERE database = ?", c.p.CH.Database)
	var delay uint64
	if err := row.Scan(&delay); err != nil {
		// 비복제 환경(system.replicas 빈 결과)은 지연 0으로 취급
		if strings.Contains(err.Error(), "no rows") {
			return 0, nil
		}
		return 0, err
	}
	return delay, nil
}

// ---------- INSERT + readback ----------

type InsertResult struct {
	Inserted int64 `json:"inserted"`
	Batches  int   `json:"batches"`
}

func (c *Client) InsertRows(ctx context.Context, rows []model.Row, batchSize int, progress func(done int64)) (*InsertResult, error) {
	cv, err := ident(c.p.CheckvalueTable)
	if err != nil {
		return nil, err
	}
	if batchSize <= 0 {
		batchSize = 100000
	}
	res := &InsertResult{}
	stmt := fmt.Sprintf("INSERT INTO %s.%s (log_date, checkpoint_id, raw_data, data)", c.db, cv)
	for start := 0; start < len(rows); start += batchSize {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		end := start + batchSize
		if end > len(rows) {
			end = len(rows)
		}
		batch, err := c.conn.PrepareBatch(ctx, stmt)
		if err != nil {
			return res, err
		}
		for _, r := range rows[start:end] {
			if err := batch.Append(r.LogDate, r.CheckpointID, r.RawData, r.Data); err != nil {
				return res, err
			}
		}
		if err := batch.Send(); err != nil {
			return res, err
		}
		res.Inserted += int64(end - start)
		res.Batches++
		if progress != nil {
			progress(res.Inserted)
		}
	}
	return res, nil
}

// CountRange는 readback용: 해당 (cp, 시간 범위)의 checkvalue 행 수.
func (c *Client) CountRange(ctx context.Context, cp int64, from, to time.Time) (int64, error) {
	cv, _ := ident(c.p.CheckvalueTable)
	row := c.conn.QueryRow(ctx, fmt.Sprintf(
		"SELECT count() FROM %s.%s WHERE checkpoint_id = ? AND log_date >= ? AND log_date < ?", c.db, cv),
		cp, from, to)
	var n uint64
	if err := row.Scan(&n); err != nil {
		return 0, err
	}
	return int64(n), nil
}

// DuplicateRows는 카나리: 해당 범위의 완전 동일 행(중복 적재) 수를 반환한다.
// 재실행 dedup 무시/이중 적재 양쪽을 이 지표로 잡는다.
func (c *Client) DuplicateRows(ctx context.Context, cp int64, from, to time.Time) (int64, error) {
	cv, _ := ident(c.p.CheckvalueTable)
	row := c.conn.QueryRow(ctx, fmt.Sprintf(`
SELECT coalesce(sum(cnt - 1), 0) FROM (
  SELECT count() AS cnt FROM %s.%s
  WHERE checkpoint_id = ? AND log_date >= ? AND log_date < ?
  GROUP BY checkpoint_id, log_date, cityHash64(data)
  HAVING cnt > 1
)`, c.db, cv), cp, from, to)
	var n uint64
	if err := row.Scan(&n); err != nil {
		return 0, err
	}
	return int64(n), nil
}

// ---------- 검증용 조회 ----------

type HourAgg struct {
	Hour    int
	Count   int64
	Min     float64
	Max     float64
	Avg     float64
	MaxTime *time.Time
}

// RawHourly는 checkvalue 원본을 시간대별로 재집계한다(L1 원천).
// max_time은 제품 동률 규칙과 동일: argMax(log_date, (raw_data, bitNot(toUInt64(log_date)))).
func (c *Client) RawHourly(ctx context.Context, cp int64, from, to time.Time) ([]HourAgg, error) {
	return c.rawHourly(ctx, cp, from, to, false)
}

// RawHourlyFinite는 제품의 checkvalue 폴백 경로와 같은 조건(isFinite)으로 재집계한다.
// NaN을 섞은 시나리오에서 "폴백 경로가 만들어야 할 값"의 원천이 된다.
func (c *Client) RawHourlyFinite(ctx context.Context, cp int64, from, to time.Time) ([]HourAgg, error) {
	return c.rawHourly(ctx, cp, from, to, true)
}

func (c *Client) rawHourly(ctx context.Context, cp int64, from, to time.Time, finiteOnly bool) ([]HourAgg, error) {
	cv, _ := ident(c.p.CheckvalueTable)
	filter := ""
	if finiteOnly {
		filter = " AND isFinite(raw_data)"
	}
	rows, err := c.conn.Query(ctx, fmt.Sprintf(`
SELECT toHour(log_date) AS h, count() AS cnt,
       min(raw_data) AS mn, max(raw_data) AS mx, avg(raw_data) AS av,
       argMax(log_date, tuple(raw_data, bitNot(toUInt64(log_date)))) AS mt
FROM %s.%s
WHERE checkpoint_id = ? AND log_date >= ? AND log_date < ?%s
GROUP BY h ORDER BY h`, c.db, cv, filter), cp, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HourAgg
	for rows.Next() {
		var h uint8
		var cnt uint64
		var a HourAgg
		var mt time.Time
		if err := rows.Scan(&h, &cnt, &a.Min, &a.Max, &a.Avg, &mt); err != nil {
			return nil, err
		}
		a.Hour = int(h)
		a.Count = int64(cnt)
		a.MaxTime = &mt
		out = append(out, a)
	}
	return out, rows.Err()
}

// MVHourly는 daily_stats(CH) MV를 시간대별로 조회한다(L1 대상).
func (c *Client) MVHourly(ctx context.Context, cp int64, date string, hasMaxTime bool) ([]HourAgg, error) {
	mv, _ := ident(c.p.DailyStatsCHTable)
	maxTimeExpr := "toDateTime64(0, 3)"
	if hasMaxTime {
		maxTimeExpr = "argMaxMerge(max_time)"
	}
	rows, err := c.conn.Query(ctx, fmt.Sprintf(`
SELECT log_hour AS h, countMerge(count_value) AS cnt,
       minMerge(min_value) AS mn, maxMerge(max_value) AS mx, avgMerge(avg_value) AS av,
       %s AS mt
FROM %s.%s
WHERE checkpoint_id = ? AND log_date = toDate(?)
GROUP BY h ORDER BY h`, maxTimeExpr, c.db, mv), cp, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HourAgg
	for rows.Next() {
		var h uint8
		var cnt uint64
		var a HourAgg
		var mt time.Time
		if err := rows.Scan(&h, &cnt, &a.Min, &a.Max, &a.Avg, &mt); err != nil {
			return nil, err
		}
		a.Hour = int(h)
		a.Count = int64(cnt)
		if hasMaxTime {
			a.MaxTime = &mt
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
