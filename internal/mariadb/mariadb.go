// Package mariadb는 MariaDB 접근을 담당한다: checkpoint 조회, 제품 통계(daily/hourly) 조회,
// 재생성 유도 체크리스트 생성. 컬럼명이 배포본마다 다를 수 있어 실제 스키마를 읽어 적응한다.
package mariadb

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

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
	db *sql.DB
	p  profile.Profile
}

func Open(p profile.Profile) (*Client, error) {
	p.ApplyDefaults()
	cfg := mysql.NewConfig()
	cfg.User = p.Maria.User
	cfg.Passwd = p.Maria.Password
	cfg.Net = "tcp"
	cfg.Addr = fmt.Sprintf("%s:%d", p.Maria.Host, p.Maria.Port)
	cfg.DBName = p.Maria.Database
	cfg.Timeout = 8 * time.Second
	cfg.ReadTimeout = 120 * time.Second
	cfg.ParseTime = false // 시간값은 문자열/epoch으로 받는다(타임존 왜곡 방지)
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	return &Client{db: db, p: p}, nil
}

func (c *Client) Close() error { return c.db.Close() }

// loc는 프로파일 타임존이다. DATETIME 형식 max_time을 epoch으로 옮길 때 쓴다.
func (c *Client) loc() *time.Location {
	if l, err := time.LoadLocation(c.p.Timezone); err == nil {
		return l
	}
	return time.Local
}

func (c *Client) Ping(ctx context.Context) error { return c.db.PingContext(ctx) }

func (c *Client) columns(ctx context.Context, table string) ([]string, error) {
	t, err := ident(table)
	if err != nil {
		return nil, err
	}
	rows, err := c.db.QueryContext(ctx, "SHOW COLUMNS FROM "+t)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	vals := make([]any, len(cols))
	raw := make([]sql.RawBytes, len(cols))
	for i := range vals {
		vals[i] = &raw[i]
	}
	var out []string
	for rows.Next() {
		if err := rows.Scan(vals...); err != nil {
			return nil, err
		}
		out = append(out, string(raw[0]))
	}
	return out, rows.Err()
}

// ---------- checkpoint 조회 ----------

type Checkpoint struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	DriverCode    string `json:"driverCode"`
	Enabled       string `json:"enabled"`
	Flag          string `json:"flag"`
	EnableMonitor string `json:"enableMonitor"`
}

// CheckpointFilter는 대상 검색 조건이다.
// Flag/EnableMonitor는 ""=조건 없음, 그 외에는 그 값과 정확히 일치하는 행만 남긴다
// (운영에서 의미 있는 대상은 flag=1 AND enable_monitor=1 이라 화면 기본값이 그것이다).
type CheckpointFilter struct {
	Search        string
	Flag          string
	EnableMonitor string
	Limit         int
	Offset        int
}

// CheckpointPage는 한 페이지분 결과다. Total은 조건에 걸린 전체 건수(페이지 계산용).
// HasFlag/HasMonitor가 false면 그 컬럼이 이 배포본 스키마에 없다는 뜻이고,
// 그때는 조건을 걸지 않는다 — 없는 컬럼으로 거른 척하면 "0건"이 조건 결과로 오독된다.
type CheckpointPage struct {
	Items      []Checkpoint
	Columns    []string
	Total      int64
	HasFlag    bool
	HasMonitor bool
	// HasEnabled=false면 이 스키마엔 enabled류 컬럼이 없다(빈 열을 그리지 않기 위함).
	HasEnabled bool
}

func pick(cols []string, candidates ...string) string {
	set := map[string]bool{}
	for _, c := range cols {
		set[strings.ToLower(c)] = true
	}
	for _, cand := range candidates {
		if set[strings.ToLower(cand)] {
			return cand
		}
	}
	return ""
}

// checkpointWhere는 검색 조건을 WHERE 절로 만든다(목록·전체 id 조회가 같은 규칙을 쓴다).
// 🔑 검색어(id 또는 이름)는 OR 묶음이라 반드시 괄호로 싸야 한다 — 없으면
// "flag=1 AND id=? OR name LIKE ?"가 되어 이름 검색 갈래에 필터가 안 걸린다.
// 🔑 flag/enable_monitor 값은 숫자로 넘긴다 — BIT 컬럼에 문자열 '1'을 비교하면
// 배포본마다 변환 규칙이 달라 "조건을 걸었는데 안 걸린" 결과가 나올 수 있다.
func (c *Client) checkpointWhere(cols []string, idCol string, f CheckpointFilter) (string, []any, error) {
	nameCol := pick(cols, "name", "checkpoint_name", "display_name", "title")
	flagCol := pick(cols, "flag")
	monCol := pick(cols, "enable_monitor", "enablemonitor", "monitor_enable", "monitoring")
	var conds []string
	var args []any
	if s := strings.TrimSpace(f.Search); s != "" {
		var or []string
		if _, err := strconv.ParseInt(s, 10, 64); err == nil {
			or = append(or, idCol+" = ?")
			args = append(args, s)
		}
		if nameCol != "" {
			or = append(or, nameCol+" LIKE ?")
			args = append(args, "%"+s+"%")
		}
		if len(or) > 0 {
			conds = append(conds, "("+strings.Join(or, " OR ")+")")
		}
	}
	addNumCond := func(col, val string) error {
		if col == "" || val == "" {
			return nil
		}
		n, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			return fmt.Errorf("조건 값 %q는 숫자가 아닙니다", val)
		}
		conds = append(conds, col+" = ?")
		args = append(args, n)
		return nil
	}
	if err := addNumCond(flagCol, f.Flag); err != nil {
		return "", nil, err
	}
	if err := addNumCond(monCol, f.EnableMonitor); err != nil {
		return "", nil, err
	}
	if len(conds) == 0 {
		return "", nil, nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args, nil
}

// ListCheckpoints는 checkpoint 테이블에서 대상 후보를 조회한다.
// id/name/driver/flag/enable_monitor 컬럼명을 스키마에서 탐지한다(배포본 차이 흡수).
func (c *Client) ListCheckpoints(ctx context.Context, f CheckpointFilter) (*CheckpointPage, error) {
	table, err := ident(c.p.CheckpointTable)
	if err != nil {
		return nil, err
	}
	cols, err := c.columns(ctx, table)
	if err != nil {
		return nil, fmt.Errorf("%s 테이블 조회 실패: %w", table, err)
	}
	page := &CheckpointPage{Columns: cols}
	idCol := pick(cols, "checkpoint_id", "id")
	nameCol := pick(cols, "name", "checkpoint_name", "display_name", "title")
	drvCol := pick(cols, "driver_code", "driver", "drivercode")
	enCol := pick(cols, "enabled", "use_yn", "is_use", "flag", "status")
	flagCol := pick(cols, "flag")
	monCol := pick(cols, "enable_monitor", "enablemonitor", "monitor_enable", "monitoring")
	if enCol == flagCol {
		enCol = "" // flag는 전용 열로 따로 보여준다 — 같은 값을 두 열에 중복 표시하지 않는다
	}
	if idCol == "" {
		return page, fmt.Errorf("%s에서 id 컬럼(checkpoint_id/id)을 찾지 못했습니다. 실제 컬럼: %s", table, strings.Join(cols, ", "))
	}
	page.HasFlag = flagCol != ""
	page.HasMonitor = monCol != ""
	page.HasEnabled = enCol != ""

	// flag/enable_monitor는 BIT(1)인 배포본이 있다 — 그대로 읽으면 원시 바이트(0x01)가
	// 그대로 화면에 뜬다. 표시용으로만 숫자로 변환하고(+0), 조건은 원본 컬럼에
	// 걸어 인덱스를 살린다.
	num := func(col string) string {
		if col == "" {
			return "''"
		}
		return "(" + col + " + 0)"
	}
	sel := []string{idCol}
	for _, col := range []string{nameCol, drvCol, enCol} {
		if col != "" {
			sel = append(sel, col)
		} else {
			sel = append(sel, "''")
		}
	}
	sel = append(sel, num(flagCol), num(monCol))

	where, args, err := c.checkpointWhere(cols, idCol, f)
	if err != nil {
		return page, err
	}

	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}

	if err := c.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+where, args...).Scan(&page.Total); err != nil {
		return page, err
	}
	q := fmt.Sprintf("SELECT %s FROM %s%s ORDER BY %s LIMIT %d OFFSET %d",
		strings.Join(sel, ", "), table, where, idCol, limit, offset)
	rows, err := c.db.QueryContext(ctx, q, args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var name, drv, en, flag, mon sql.NullString
		if err := rows.Scan(&id, &name, &drv, &en, &flag, &mon); err != nil {
			return page, err
		}
		page.Items = append(page.Items, Checkpoint{
			ID: id, Name: name.String, DriverCode: drv.String, Enabled: en.String,
			Flag: flag.String, EnableMonitor: mon.String,
		})
	}
	return page, rows.Err()
}

// ListCheckpointIDs는 같은 조건에 걸리는 checkpoint id 전체를 돌려준다("조회 결과 전체 선택"용).
// 화면 한 페이지가 아니라 조건 전체가 대상이므로 상한(max)을 둔다 — 넘으면 조건을 좁히게 한다
// (수만 개 cp를 한 번에 고르면 주입량이 통제 밖이고, 칩 렌더링도 무의미해진다).
func (c *Client) ListCheckpointIDs(ctx context.Context, f CheckpointFilter, max int) ([]int64, int64, error) {
	page, err := c.ListCheckpoints(ctx, CheckpointFilter{
		Search: f.Search, Flag: f.Flag, EnableMonitor: f.EnableMonitor, Limit: 1,
	})
	if err != nil {
		return nil, 0, err
	}
	if max <= 0 {
		max = 20000
	}
	if page.Total > int64(max) {
		return nil, page.Total, fmt.Errorf("조건에 걸린 대상이 %d건입니다 — 한 번에 고를 수 있는 상한(%d건)을 넘었습니다. 조건이나 검색어로 범위를 좁히세요", page.Total, max)
	}

	table, err := ident(c.p.CheckpointTable)
	if err != nil {
		return nil, 0, err
	}
	cols, err := c.columns(ctx, table)
	if err != nil {
		return nil, 0, err
	}
	idCol := pick(cols, "checkpoint_id", "id")
	if idCol == "" {
		return nil, 0, fmt.Errorf("%s에서 id 컬럼을 찾지 못했습니다", table)
	}
	where, args, err := c.checkpointWhere(cols, idCol, f)
	if err != nil {
		return nil, 0, err
	}
	rows, err := c.db.QueryContext(ctx,
		fmt.Sprintf("SELECT %s FROM %s%s ORDER BY %s", idCol, table, where, idCol), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, 0, err
		}
		out = append(out, id)
	}
	return out, page.Total, rows.Err()
}

// ---------- 제품 통계 조회 ----------

// DailyRow는 MariaDB daily_stats 한 행이다.
// Hour* 맵의 키는 CH hour H (0~23) — 컬럼 _NN(종료 라벨)에서 H=NN-1로 역매핑해 담는다.
type DailyRow struct {
	CheckpointID  int64
	Date          string
	Min, Max, Avg *float64 // 일별 대푯값
	MaxTimeMs     *int64   // epoch ms (해석 실패 시 nil, 원문은 MaxTimeRaw)
	MaxTimeRaw    string   // 해석하지 못한 max_time 원문(진단용)
	HourMin       map[int]*float64
	HourMax       map[int]*float64
	HourAvg       map[int]*float64
}

var hourColRe = regexp.MustCompile(`^(min|max|avg)_value_(\d{2})$`)

// max_time 컬럼 타입은 배포본마다 다르다(epoch ms BIGINT / DATETIME / TIMESTAMP).
// epoch만 파싱하고 실패를 nil로 남기면 대조가 조용히 사라지므로, 형식을 순서대로 시도하고
// 실패 시 원문을 돌려준다(원문은 리포트에 실측값으로 표시된다).
var maxTimeLayouts = []string{
	"2006-01-02 15:04:05.999999",
	"2006-01-02T15:04:05.999999",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05Z07:00",
}

func parseMaxTime(text string, loc *time.Location) *int64 {
	s := strings.TrimSpace(text)
	if s == "" || strings.EqualFold(s, "null") {
		return nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return &n
	}
	if loc == nil {
		loc = time.Local
	}
	for _, layout := range maxTimeLayouts {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			ms := t.UnixMilli()
			return &ms
		}
	}
	return nil
}

// DailyStats는 (cp, 날짜)의 daily_stats 행을 읽는다. 없으면 nil.
func (c *Client) DailyStats(ctx context.Context, cp int64, date string) (*DailyRow, error) {
	table, err := ident(c.p.DailyStatsTable)
	if err != nil {
		return nil, err
	}
	cols, err := c.columns(ctx, table)
	if err != nil {
		return nil, err
	}
	dateCol := pick(cols, "log_date", "date", "stat_date")
	cpCol := pick(cols, "checkpoint_id")
	if dateCol == "" || cpCol == "" {
		return nil, fmt.Errorf("%s에서 log_date/checkpoint_id 컬럼을 찾지 못했습니다. 실제 컬럼: %s", table, strings.Join(cols, ", "))
	}

	q := fmt.Sprintf("SELECT * FROM %s WHERE %s = ? AND %s = ?", table, cpCol, dateCol)
	rows, err := c.db.QueryContext(ctx, q, cp, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	colNames, _ := rows.Columns()
	if !rows.Next() {
		return nil, nil // 행 없음 = 통계 미생성
	}
	raw := make([]sql.RawBytes, len(colNames))
	vals := make([]any, len(colNames))
	for i := range vals {
		vals[i] = &raw[i]
	}
	if err := rows.Scan(vals...); err != nil {
		return nil, err
	}

	dr := &DailyRow{CheckpointID: cp, Date: date,
		HourMin: map[int]*float64{}, HourMax: map[int]*float64{}, HourAvg: map[int]*float64{}}
	for i, name := range colNames {
		lower := strings.ToLower(name)
		if raw[i] == nil {
			continue // NULL
		}
		text := string(raw[i])
		if m := hourColRe.FindStringSubmatch(lower); m != nil {
			nn, _ := strconv.Atoi(m[2])
			h := nn - 1 // 종료 라벨 _NN → CH hour H = NN-1 (최대 함정: 이 역매핑을 틀리면 전수 불일치)
			if h < 0 || h > 23 {
				continue
			}
			if f, err := strconv.ParseFloat(text, 64); err == nil {
				switch m[1] {
				case "min":
					dr.HourMin[h] = &f
				case "max":
					dr.HourMax[h] = &f
				case "avg":
					dr.HourAvg[h] = &f
				}
			}
			continue
		}
		switch lower {
		case "min_value":
			if f, err := strconv.ParseFloat(text, 64); err == nil {
				dr.Min = &f
			}
		case "max_value":
			if f, err := strconv.ParseFloat(text, 64); err == nil {
				dr.Max = &f
			}
		case "avg_value":
			if f, err := strconv.ParseFloat(text, 64); err == nil {
				dr.Avg = &f
			}
		case "max_time":
			dr.MaxTimeMs = parseMaxTime(text, c.loc())
			if dr.MaxTimeMs == nil {
				dr.MaxTimeRaw = text
			}
		}
	}
	return dr, nil
}

// HourlyRow는 hourly_checkpoint_data_log 한 행이다. hour = 시작 라벨(H↔H, daily와 반대).
type HourlyRow struct {
	Hour          int
	Min, Max, Avg *float64
	MaxTimeMs     *int64 // epoch ms (해석 실패 시 nil, 원문은 MaxTimeRaw)
	MaxTimeRaw    string
}

func (c *Client) HourlyStats(ctx context.Context, cp int64, date string) ([]HourlyRow, error) {
	table, err := ident(c.p.HourlyTable)
	if err != nil {
		return nil, err
	}
	cols, err := c.columns(ctx, table)
	if err != nil {
		return nil, err
	}
	dateCol := pick(cols, "log_date", "date")
	cpCol := pick(cols, "checkpoint_id")
	hourCol := pick(cols, "hour", "log_hour")
	minCol := pick(cols, "min_value", "min")
	maxCol := pick(cols, "max_value", "max")
	avgCol := pick(cols, "avg_value", "avg")
	mtCol := pick(cols, "max_time")
	if dateCol == "" || cpCol == "" || hourCol == "" {
		return nil, fmt.Errorf("%s에서 필수 컬럼을 찾지 못했습니다. 실제 컬럼: %s", table, strings.Join(cols, ", "))
	}
	// 값 컬럼을 못 찾았는데 리터럴 NULL로 SELECT하면 스키마 불일치가 "값 비교 0건 + 전 셀 통과"로
	// 둔갑한다. 대조 대상 자체가 없는 것이므로 조회 실패로 알린다.
	if minCol == "" || maxCol == "" || avgCol == "" {
		return nil, fmt.Errorf("%s에서 값 컬럼(min/max/avg)을 찾지 못해 대조할 수 없습니다. 실제 컬럼: %s", table, strings.Join(cols, ", "))
	}
	sel := []string{hourCol, minCol, maxCol, avgCol}
	if mtCol != "" {
		sel = append(sel, mtCol)
	} else {
		sel = append(sel, "NULL")
	}
	q := fmt.Sprintf("SELECT %s FROM %s WHERE %s = ? AND %s = ? ORDER BY %s",
		strings.Join(sel, ", "), table, cpCol, dateCol, hourCol)
	rows, err := c.db.QueryContext(ctx, q, cp, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HourlyRow
	for rows.Next() {
		var hourText sql.NullString
		var mn, mx, av sql.NullFloat64
		// max_time은 배포본에 따라 BIGINT epoch일 수도 DATETIME일 수도 있다.
		// NullInt64로 받으면 DATETIME 배포본에서 Scan 자체가 에러가 되므로 문자열로 받는다.
		var mt sql.NullString
		if err := rows.Scan(&hourText, &mn, &mx, &av, &mt); err != nil {
			return nil, err
		}
		h, err := strconv.Atoi(strings.TrimSpace(hourText.String))
		if err != nil {
			continue
		}
		hr := HourlyRow{Hour: h}
		if mn.Valid {
			hr.Min = &mn.Float64
		}
		if mx.Valid {
			hr.Max = &mx.Float64
		}
		if av.Valid {
			hr.Avg = &av.Float64
		}
		if mt.Valid {
			hr.MaxTimeMs = parseMaxTime(mt.String, c.loc())
			if hr.MaxTimeMs == nil {
				hr.MaxTimeRaw = mt.String
			}
		}
		out = append(out, hr)
	}
	return out, rows.Err()
}

// ---------- 재생성 유도 체크리스트 ----------

// RegenChecklist는 과거 날짜 재생성을 유도하는 수동 절차 문서를 생성한다.
// 도구는 삭제를 실행하지 않는다 — analyzer의 멱등 스킵(행+마커 존재 시 already been generated)을
// 뚫으려면 이 절차가 필요하다는 것을 사용자에게 안내만 한다(계획서 §7.3).
// DB 연결이 필요 없다(프로파일의 테이블명만 사용).
func RegenChecklist(p profile.Profile, cp []int64, dates []string) string {
	p.ApplyDefaults()
	c := &Client{p: p}
	return c.regenChecklist(cp, dates)
}

func (c *Client) regenChecklist(cp []int64, dates []string) string {
	var b strings.Builder
	b.WriteString("# 제품 통계 재생성 유도 체크리스트\n\n")
	b.WriteString("analyzer는 해당 날짜의 MariaDB 행 + liz_log 마커가 있으면 재생성을 스킵합니다(멱등).\n")
	b.WriteString("아래 절차를 QA 환경에서 작업자가 직접 실행하세요. 이 도구는 삭제를 실행하지 않습니다.\n\n")
	b.WriteString("## 1. 대상 날짜의 통계 행 삭제 (확인 후 실행)\n\n```sql\n")
	cpList := make([]string, len(cp))
	for i, id := range cp {
		cpList[i] = strconv.FormatInt(id, 10)
	}
	for _, d := range dates {
		fmt.Fprintf(&b, "-- %s\nDELETE FROM %s WHERE log_date = '%s' AND checkpoint_id IN (%s);\n",
			d, c.p.DailyStatsTable, d, strings.Join(cpList, ", "))
	}
	b.WriteString("```\n\n")
	b.WriteString("주의: 특정 checkpoint만 재생성하는 개념이 없다면(전일 단위 재생성) 날짜 전체 행 삭제가 필요할 수 있습니다. 운영 데이터가 섞인 환경에서는 절대 전체 삭제하지 마세요.\n\n")
	b.WriteString("## 2. liz_log 생성 마커 삭제\n\n")
	b.WriteString("해당 날짜의 daily stats 생성 마커(liz_log)를 함께 삭제해야 missing-loop이 누락으로 인지합니다.\n\n")
	b.WriteString("## 3. 재생성 트리거 (ACTIVE/STANDBY 주의)\n\n")
	b.WriteString("1. `daily.stats.generate.time` = 현재+5분으로 저장\n")
	b.WriteString("2. STANDBY analyzer 먼저 재기동\n")
	b.WriteString("3. ACTIVE analyzer 재기동 (역할이 반대 노드로 이동)\n")
	b.WriteString("4. 새 ACTIVE에서 발화 관찰\n")
	b.WriteString("5. 원복은 양 노드 모두\n\n")
	b.WriteString("## 4. 관문 주의사항\n\n")
	b.WriteString("- `verifyAllLogSaved` 게이트: 당일 수집이 확인되지 않으면 30분(1800초) 대기 후 재시도 없이 종료\n")
	b.WriteString("- 성공 로그 문구: `start to make ... daily_stats` → `total of N checkpoint statistics ... (generated from daily_stats)`\n")
	b.WriteString("- MV 0행 폴백 문구: `daily_stats table has no record for <날짜>, try again with checkvalue`\n\n")
	b.WriteString("## 5. 완료 후\n\n")
	b.WriteString("이 도구의 Verify 화면에서 검증을 실행하세요 (복제 지연 가드 통과 필요).\n")
	return b.String()
}
