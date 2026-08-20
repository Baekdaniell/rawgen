package main

// app.go는 Wails IPC facade만 담당한다. 핵심 로직은 전부 internal 패키지(코어)에
// 있다 — "Wails는 얇게, core는 UI를 모르게".

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
	_ "time/tzdata" // Windows에는 시스템 tzdata가 없어 임베드 필수

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"rawgen/internal/chdb"
	"rawgen/internal/e2e"
	"rawgen/internal/executor"
	"rawgen/internal/mariadb"
	"rawgen/internal/model"
	"rawgen/internal/planner"
	"rawgen/internal/profile"
	"rawgen/internal/report"
	"rawgen/internal/verify"
)

const appVersion = "0.3.0"

type App struct {
	ctx    context.Context
	store  *profile.Store
	mu     sync.Mutex
	cancel context.CancelFunc

	cancelE2E context.CancelFunc

	// busy는 실행 상호 배타 게이트다: "" | "generate" | "e2e".
	// E2E 마일스톤 대기 중 Generate로 같은 (cp×날짜)에 추가 INSERT하면 expected가
	// 깨져 남은 셀이 전부 오염되고, 동시 Verify는 lastVerify를 덮어쓴다.
	busy       string
	stopAwake  func()

	lastPreview *model.Preview
	lastRun     *executor.RunResult
	lastVerify  *verify.Result
	lastE2E     *e2e.Result
}

// setBusy는 실행 게이트를 잡는다. 이미 다른 실행이 있으면 에러.
// 잡는 동안 시스템 절전을 막는다(장기 E2E가 절전으로 마일스톤을 놓치는 것 방지).
func (a *App) setBusy(kind string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy != "" {
		return fmt.Errorf("%s 실행 중에는 %s를 시작할 수 없습니다 — 중단 후 다시 시도하세요", busyLabel(a.busy), busyLabel(kind))
	}
	a.busy = kind
	a.stopAwake = startKeepAwake()
	return nil
}

func (a *App) clearBusy() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.busy = ""
	if a.stopAwake != nil {
		a.stopAwake()
		a.stopAwake = nil
	}
}

func busyLabel(kind string) string {
	switch kind {
	case "generate":
		return "Generate(주입)"
	case "e2e":
		return "E2E"
	case "verify":
		return "Verify"
	}
	return kind
}

// Busy는 프런트가 현재 실행 상태를 조회할 때 쓴다.
func (a *App) Busy() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.busy
}

func NewApp() *App {
	return &App{}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	store, err := profile.NewStore()
	if err == nil {
		a.store = store
	}
}

type AppInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func (a *App) Info() AppInfo {
	return AppInfo{Name: "rawgen", Version: appVersion}
}

// ---------- 프로파일 ----------

func (a *App) ListProfiles() ([]profile.Profile, error) {
	return a.store.List()
}

// SaveProfile은 프로파일을 저장한다. 테이블명 오버라이드는 UI 폼에 없을 수 있어
// 빈 값으로 오면 기존 값을 유지한다 — 그냥 덮으면 배포본 테이블명 설정이 조용히
// 표준명으로 되돌아가고, 그 결과 hourly 조회가 실패한다.
func (a *App) SaveProfile(p profile.Profile) (profile.Profile, error) {
	if p.ID != "" {
		if old, err := a.store.Get(p.ID); err == nil {
			mergeTableOverrides(&p, old)
		}
	}
	return a.store.Upsert(p)
}

func mergeTableOverrides(p *profile.Profile, old profile.Profile) {
	pairs := []struct{ dst *string; src string }{
		{&p.CheckvalueTable, old.CheckvalueTable},
		{&p.DailyStatsCHTable, old.DailyStatsCHTable},
		{&p.DailyStatsTable, old.DailyStatsTable},
		{&p.HourlyTable, old.HourlyTable},
		{&p.CheckpointTable, old.CheckpointTable},
		{&p.ExcludeDateTable, old.ExcludeDateTable},
	}
	for _, pr := range pairs {
		if *pr.dst == "" {
			*pr.dst = pr.src
		}
	}
}

func (a *App) DeleteProfile(id string) error {
	return a.store.Delete(id)
}

func (a *App) ExportProfiles() (string, error) {
	return a.store.Export()
}

// ExportProfilesFile은 세션 목록을 파일로 저장한다(비밀번호는 placeholder).
// 엔지니어끼리 접속 정보를 주고받는 통로다 — 받는 쪽은 ImportProfilesFile로 읽는다.
func (a *App) ExportProfilesFile() (string, error) {
	text, err := a.store.Export()
	if err != nil {
		return "", err
	}
	path, err := wruntime.SaveFileDialog(a.ctx, wruntime.SaveDialogOptions{
		Title:           "세션 내보내기",
		DefaultFilename: "rawgen-sessions.json",
		Filters:         []wruntime.FileFilter{{DisplayName: "JSON (*.json)", Pattern: "*.json"}},
	})
	if err != nil || path == "" {
		return "", err
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// ImportProfiles는 붙여넣은 JSON에서 세션을 등록한다(이름이 같으면 갱신).
func (a *App) ImportProfiles(text string) (profile.ImportSummary, error) {
	return a.store.Import(text)
}

// ImportProfilesFile은 파일에서 세션을 등록한다.
func (a *App) ImportProfilesFile() (profile.ImportSummary, error) {
	path, err := wruntime.OpenFileDialog(a.ctx, wruntime.OpenDialogOptions{
		Title:   "세션 JSON 가져오기",
		Filters: []wruntime.FileFilter{{DisplayName: "JSON (*.json)", Pattern: "*.json"}},
	})
	if err != nil || path == "" {
		return profile.ImportSummary{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return profile.ImportSummary{}, err
	}
	return a.store.Import(string(data))
}

// DuplicateProfile은 선택한 세션을 복제한다.
func (a *App) DuplicateProfile(id string) (profile.Profile, error) {
	return a.store.Duplicate(id)
}

// useProfile은 세션을 읽으면서 "마지막 사용"을 갱신한다. 세션이 여러 개일 때
// 목록에서 최근에 쓰던 것을 바로 알아보게 하려는 표시다.
func (a *App) useProfile(id string) (profile.Profile, error) {
	p, err := a.store.Get(id)
	if err == nil {
		a.store.Touch(p.ID)
	}
	return p, err
}

// UseProfile은 "이 세션을 지금 쓰고 있다"를 기록한다(목록의 마지막 사용 표시).
func (a *App) UseProfile(id string) {
	if id != "" {
		a.store.Touch(id)
	}
}

type TestResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

func (a *App) TestMaria(profileID string) TestResult {
	p, err := a.store.Get(profileID)
	if err != nil {
		return TestResult{Message: err.Error()}
	}
	c, err := mariadb.Open(p)
	if err != nil {
		return TestResult{Message: err.Error()}
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
	defer cancel()
	if err := c.Ping(ctx); err != nil {
		return TestResult{Message: err.Error()}
	}
	return TestResult{OK: true, Message: fmt.Sprintf("MariaDB %s:%d/%s 연결 성공", p.Maria.Host, p.Maria.Port, p.Maria.Database)}
}

func (a *App) TestCH(profileID string) TestResult {
	p, err := a.store.Get(profileID)
	if err != nil {
		return TestResult{Message: err.Error()}
	}
	c, err := chdb.Open(p)
	if err != nil {
		return TestResult{Message: err.Error()}
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
	defer cancel()
	if err := c.Ping(ctx); err != nil {
		return TestResult{Message: err.Error()}
	}
	return TestResult{OK: true, Message: fmt.Sprintf("ClickHouse %s:%d/%s 연결 성공", p.CH.Host, p.CH.Port, p.CH.Database)}
}

func (a *App) Discover(profileID string) (*chdb.Discovery, error) {
	p, err := a.useProfile(profileID)
	if err != nil {
		return nil, err
	}
	c, err := chdb.Open(p)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()
	d, err := c.Discover(ctx)
	if err != nil {
		return nil, err
	}
	// verify 60초 가드 사전 확인용. 실패(비복제 환경 등)면 -1 = 측정 불가
	if delay, derr := c.ReplicationDelay(ctx); derr == nil {
		d.ReplicationDelay = int64(delay)
	} else {
		d.ReplicationDelay = -1
	}
	return d, nil
}

// ---------- 대상(checkpoint) ----------

type CheckpointList struct {
	Items   []mariadb.Checkpoint `json:"items"`
	Columns []string             `json:"columns"`
}

func (a *App) ListCheckpoints(profileID, search string) (*CheckpointList, error) {
	p, err := a.store.Get(profileID)
	if err != nil {
		return nil, err
	}
	c, err := mariadb.Open(p)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()
	items, cols, err := c.ListCheckpoints(ctx, search, 100)
	if err != nil {
		return nil, err
	}
	return &CheckpointList{Items: items, Columns: cols}, nil
}

// ---------- Scenario 파일 ----------

// ScenarioFile은 파일에서 불러온 시나리오와 검증 결과를 함께 담는다.
// 파싱은 됐지만 Validate가 실패한 경우에도 시나리오를 돌려줘서(Problem에 사유)
// 사용자가 폼에서 고칠 수 있게 한다.
type ScenarioFile struct {
	Scenario model.Scenario `json:"scenario"`
	Path     string         `json:"path"`
	Warnings []string       `json:"warnings"`
	Problem  string         `json:"problem,omitempty"`
}

// LoadScenarioFile은 열기 대화상자로 시나리오 JSON을 불러온다.
// 취소하면 nil을 돌려준다(오류 아님).
func (a *App) LoadScenarioFile() (*ScenarioFile, error) {
	path, err := wruntime.OpenFileDialog(a.ctx, wruntime.OpenDialogOptions{
		Title:   "시나리오 JSON 불러오기",
		Filters: []wruntime.FileFilter{{DisplayName: "JSON (*.json)", Pattern: "*.json"}},
	})
	if err != nil || path == "" {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s model.Scenario
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("시나리오 파싱 실패(%s): %w", path, err)
	}
	out := &ScenarioFile{Scenario: s, Path: path}
	warnings, verr := s.Validate(time.Now())
	out.Warnings = warnings
	if verr != nil {
		out.Problem = verr.Error()
	}
	return out, nil
}

// guardNaN은 NaN 주입을 두 겹으로 막는다: TestOnly 프로파일 + 전용 확인.
// NaN은 제품 통계를 경로별로 갈라놓기 때문에, 실수로 섞이면 그 (날짜×cp)는
// 이후 어떤 검증에도 클린 기준으로 쓸 수 없다.
func guardNaN(s model.Scenario, p profile.Profile, dryRun, allowNaN bool) error {
	if !s.HasNaN() || dryRun {
		return nil
	}
	if !p.TestOnly {
		return fmt.Errorf("NaN 주입은 TestOnly 세션에서만 가능합니다")
	}
	if !allowNaN {
		return fmt.Errorf("NaN 주입 확인이 필요합니다 — 시나리오에 NaN 행이 포함되어 있습니다. 주입 화면의 'NaN 주입 확인'을 체크하세요")
	}
	return nil
}

// ---------- Preview ----------

func (a *App) BuildPreview(scenarioJSON string) (*model.Preview, error) {
	var s model.Scenario
	if err := json.Unmarshal([]byte(scenarioJSON), &s); err != nil {
		return nil, fmt.Errorf("시나리오 파싱 실패: %w", err)
	}
	p, err := planner.Build(s, time.Now())
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.lastPreview = p
	a.mu.Unlock()
	return p, nil
}

// ---------- Generate ----------

// Generate는 비동기로 실행하고 진행률을 이벤트로 보낸다.
// 이벤트: gen:progress(executor.Progress), gen:done(RunResult), gen:error(string)
// allowNaN은 NaN 주입 전용 확인이다 — 실수로 켜지지 않도록 일반 확인과 분리한다.
func (a *App) Generate(profileID, scenarioJSON string, dryRun, allowNaN bool) error {
	p, err := a.useProfile(profileID)
	if err != nil {
		return err
	}
	var s model.Scenario
	if err := json.Unmarshal([]byte(scenarioJSON), &s); err != nil {
		return fmt.Errorf("시나리오 파싱 실패: %w", err)
	}
	if err := guardNaN(s, p, dryRun, allowNaN); err != nil {
		return err
	}

	if err := a.setBusy("generate"); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.mu.Lock()
	a.cancel = cancel
	a.mu.Unlock()

	go func() {
		defer func() {
			a.mu.Lock()
			a.cancel = nil
			a.mu.Unlock()
			a.clearBusy()
		}()
		res, err := executor.Run(ctx, p, s, dryRun, func(pr executor.Progress) {
			wruntime.EventsEmit(a.ctx, "gen:progress", pr)
		})
		a.mu.Lock()
		a.lastRun = res
		a.mu.Unlock()
		if err != nil {
			wruntime.EventsEmit(a.ctx, "gen:error", err.Error())
			return
		}
		wruntime.EventsEmit(a.ctx, "gen:done", res)
	}()
	return nil
}

func (a *App) CancelGenerate() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		a.cancel()
	}
}

func (a *App) History(limit int) ([]executor.RunResult, error) {
	return executor.History(limit)
}

// ---------- Regenerate ----------

func (a *App) RegenChecklist(profileID, scenarioJSON string) (string, error) {
	p, err := a.store.Get(profileID)
	if err != nil {
		return "", err
	}
	var s model.Scenario
	if err := json.Unmarshal([]byte(scenarioJSON), &s); err != nil {
		return "", err
	}
	loc, err := s.Location()
	if err != nil {
		return "", err
	}
	start, _ := time.ParseInLocation("2006-01-02", s.StartDate, loc)
	end, _ := time.ParseInLocation("2006-01-02", s.EndDate, loc)
	var dates []string
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		dates = append(dates, d.Format("2006-01-02"))
	}
	return mariadb.RegenChecklist(p, s.CheckpointIDs, dates), nil
}

// ---------- Verify ----------

func (a *App) RunVerify(profileID, scenarioJSON string, skipL2 bool) (*verify.Result, error) {
	if err := a.setBusy("verify"); err != nil {
		return nil, err
	}
	defer a.clearBusy()
	p, err := a.useProfile(profileID)
	if err != nil {
		return nil, err
	}
	var s model.Scenario
	if err := json.Unmarshal([]byte(scenarioJSON), &s); err != nil {
		return nil, fmt.Errorf("시나리오 파싱 실패: %w", err)
	}
	ctx, cancel := context.WithTimeout(a.ctx, 10*time.Minute)
	defer cancel()
	res, err := verify.Run(ctx, p, s, verify.Options{SkipL2: skipL2})
	if res != nil {
		a.mu.Lock()
		a.lastVerify = res
		a.mu.Unlock()
	}
	if err != nil {
		return res, err
	}
	return res, nil
}

// ---------- E2E ----------

// E2EOptions는 프런트가 JSON으로 넘기는 실행 옵션이다.
type E2EOptions struct {
	AllowNaN     bool `json:"allowNaN"`     // NaN 주입 확인(별도 체크박스)
	SkipGenerate bool `json:"skipGenerate"` // 이미 주입된 시나리오를 검증만
	KeepGoing    bool `json:"keepGoing"`    // L1/readback 불합격에도 계속 (진단용)
	SkipDaily    bool `json:"skipDaily"`    // 일별 셀 제외 (자정 전 종료)
	Resume       bool `json:"resume"`       // 저장 상태에서 재개
	RetryMinutes int  `json:"retryMinutes"` // 불일치 재확인 간격(분), 0=기본 10
	MaxAttempts  int  `json:"maxAttempts"`  // 셀당 판정 시도 상한, 0=기본 3
}

func e2eStatePath() (string, error) {
	dir, err := profile.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "e2e-state.json"), nil
}

func loadE2EState() *e2e.Result {
	path, err := e2eStatePath()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var r e2e.Result
	if json.Unmarshal(data, &r) != nil {
		return nil
	}
	return &r
}

// saveE2EState는 판정이 반영될 때마다 호출된다. 실패해도 실행엔 영향 없음.
func saveE2EState(r *e2e.Result) {
	path, err := e2eStatePath()
	if err != nil {
		return
	}
	if data, err := json.Marshal(r); err == nil {
		_ = os.WriteFile(path, data, 0o600)
	}
}

func discardE2EState() {
	if path, err := e2eStatePath(); err == nil {
		_ = os.Remove(path)
	}
}

// autoSaveE2EReport는 결과를 %APPDATA%\rawgen\reports\에 무조건 기록한다.
// 하루짜리 결과가 메모리에만 있다가 앱 종료로 유실되는 것을 막는다(중단 결과 포함).
func autoSaveE2EReport(r *e2e.Result) (string, error) {
	dir, err := profile.Dir()
	if err != nil {
		return "", err
	}
	rdir := filepath.Join(dir, "reports")
	if err := os.MkdirAll(rdir, 0o700); err != nil {
		return "", err
	}
	stamp := time.Now().Format("20060102-150405")
	mdPath := filepath.Join(rdir, "e2e-"+stamp+".md")
	if err := os.WriteFile(mdPath, []byte(e2e.Markdown(r)), 0o644); err != nil {
		return "", err
	}
	if data, jerr := json.MarshalIndent(r, "", "  "); jerr == nil {
		_ = os.WriteFile(filepath.Join(rdir, "e2e-"+stamp+".json"), data, 0o644)
	}
	return mdPath, nil
}

// PendingE2E는 재개 가능한 저장 상태 요약을 돌려준다(프런트 시작 시 조회).
type PendingState struct {
	Exists    bool   `json:"exists"`
	StartedAt string `json:"startedAt,omitempty"`
	Aborted   string `json:"aborted,omitempty"`
	DoneCells int    `json:"doneCells"`
	// NoCells는 "셀 계획 이전에 중단된 상태"다. 이 상태로는 재개할 수 없다
	// (셀 0개로 즉시 완주해 대조 0건 PASS가 나오기 때문).
	NoCells      bool   `json:"noCells"`
	TotalCells   int    `json:"totalCells"`
	Profile      string `json:"profile,omitempty"`
	Target       string `json:"target,omitempty"`
	ScenarioJSON string `json:"scenarioJson,omitempty"`
}

func (a *App) PendingE2E() *PendingState {
	st := loadE2EState()
	if st == nil {
		return &PendingState{}
	}
	out := &PendingState{Exists: true, StartedAt: st.StartedAt, Aborted: st.Aborted,
		TotalCells: len(st.Cells), NoCells: len(st.Cells) == 0, Profile: st.Profile, Target: st.Target}
	for _, c := range st.Cells {
		if c.Status != e2e.StatusWait && c.Status != e2e.StatusRecheck {
			out.DoneCells++
		}
	}
	if data, err := json.Marshal(st.Scenario); err == nil {
		out.ScenarioJSON = string(data)
	}
	return out
}

// DiscardE2EState는 저장 상태를 버린다(재개 배너의 "버리기").
func (a *App) DiscardE2EState() {
	discardE2EState()
}

// PreflightResult는 E2E 시작 전 사전 점검 결과다.
type PreflightResult struct {
	MariaOK  bool             `json:"mariaOk"`
	MariaMsg string           `json:"mariaMsg"`
	CHOK     bool             `json:"chOk"`
	CHMsg    string           `json:"chMsg"`
	TestOnly bool             `json:"testOnly"`
	Today    bool             `json:"today"` // 시나리오가 오늘 날짜를 포함하는가
	Plan     *e2e.PlanSummary `json:"plan,omitempty"`
	Warnings []string         `json:"warnings"`
	Fatal    []string         `json:"fatal"` // 시작하면 안 되는 사유
}

// E2EPreflight는 연결·TestOnly·날짜·셀 계획을 실행 전에 점검한다.
// 과거 날짜 시나리오로 하루치 INSERT를 낭비하고 (날짜×cp)를 오염시키는 실수 방지.
func (a *App) E2EPreflight(profileID, scenarioJSON string, skipGenerate, skipDaily bool) (*PreflightResult, error) {
	p, err := a.store.Get(profileID)
	if err != nil {
		return nil, err
	}
	var s model.Scenario
	if err := json.Unmarshal([]byte(scenarioJSON), &s); err != nil {
		return nil, fmt.Errorf("시나리오 파싱 실패: %w", err)
	}
	res := &PreflightResult{TestOnly: p.TestOnly}

	ctx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
	defer cancel()
	if c, err := mariadb.Open(p); err == nil {
		if perr := c.Ping(ctx); perr == nil {
			res.MariaOK = true
			res.MariaMsg = fmt.Sprintf("%s:%d/%s", p.Maria.Host, p.Maria.Port, p.Maria.Database)
		} else {
			res.MariaMsg = perr.Error()
		}
		c.Close()
	} else {
		res.MariaMsg = err.Error()
	}
	if c, err := chdb.Open(p); err == nil {
		if perr := c.Ping(ctx); perr == nil {
			res.CHOK = true
			res.CHMsg = fmt.Sprintf("%s:%d/%s", p.CH.Host, p.CH.Port, p.CH.Database)
		} else {
			res.CHMsg = perr.Error()
		}
		c.Close()
	} else {
		res.CHMsg = err.Error()
	}

	loc, err := s.Location()
	if err != nil {
		return nil, err
	}
	warnings, verr := s.Validate(time.Now())
	res.Warnings = warnings
	if verr != nil {
		res.Fatal = append(res.Fatal, "시나리오 오류: "+verr.Error())
		return res, nil
	}
	today := time.Now().In(loc).Format("2006-01-02")
	res.Today = s.StartDate <= today && today <= s.EndDate
	if !res.Today {
		res.Warnings = append(res.Warnings,
			"시나리오에 오늘 날짜가 없습니다 — hourly는 당일 데이터만 생성되므로 대부분 셀이 '제외(skip)'가 되고, 주입한 (날짜×cp)만 오염됩니다. E2E는 오늘 날짜 시나리오 전용입니다")
	}

	// 주입 생략이면 실제 주입 시각을 이력에서 찾는다. 시작일 00:00으로 낙관하면
	// 계획상 skip이 0개로 나와 "전부 skip" 차단이 발동하지 않는다.
	injectAt := time.Now()
	if skipGenerate {
		if t, ok := e2e.ResolveInjectAt(s, loc); ok {
			injectAt = t
		} else {
			res.Warnings = append(res.Warnings,
				"주입 생략: 이 시나리오의 성공한 주입 이력을 찾지 못했습니다 — 현재 시각 기준으로 계획하므로 이미 생성 시점이 지난 시간대는 제외(skip)됩니다")
		}
	}
	plan, err := e2e.PlanPreview(s, injectAt, skipDaily)
	if err != nil {
		return nil, err
	}
	res.Plan = plan

	if !res.MariaOK {
		res.Fatal = append(res.Fatal, "MariaDB 연결 실패: "+res.MariaMsg)
	}
	if !res.CHOK {
		res.Fatal = append(res.Fatal, "ClickHouse 연결 실패: "+res.CHMsg)
	}
	if !skipGenerate && !p.TestOnly {
		res.Fatal = append(res.Fatal, "TestOnly 세션이 아닙니다 — 실제 INSERT가 차단됩니다")
	}
	if plan.SkipCells >= plan.TotalCells {
		res.Fatal = append(res.Fatal, "모든 셀이 보존 창 밖(skip)입니다 — 판정할 것이 없습니다 (과거 날짜 시나리오)")
	}
	return res, nil
}

// RunE2E는 온종일 E2E(주입→L1→시간대별 자동 verify→리포트)를 비동기로 실행한다.
// 이벤트: e2e:progress(e2e.Progress — phase "cells"에 셀 스냅샷 포함),
// e2e:done(e2e.Result), e2e:error(string). 몇 시간~하루가 걸리는 장기 실행이다.
// 진행 상태는 매 판정마다 파일로 영속화되어 크래시 후 재개(Resume)할 수 있고,
// 결과 리포트는 완료·중단 공통으로 자동 저장된다.
func (a *App) RunE2E(profileID, scenarioJSON, optionsJSON string) error {
	p, err := a.useProfile(profileID)
	if err != nil {
		return err
	}
	var s model.Scenario
	if err := json.Unmarshal([]byte(scenarioJSON), &s); err != nil {
		return fmt.Errorf("시나리오 파싱 실패: %w", err)
	}
	var o E2EOptions
	if optionsJSON != "" {
		if err := json.Unmarshal([]byte(optionsJSON), &o); err != nil {
			return fmt.Errorf("옵션 파싱 실패: %w", err)
		}
	}
	if err := guardNaN(s, p, o.SkipGenerate || o.Resume, o.AllowNaN); err != nil {
		return err
	}
	opt := e2e.Options{
		SkipGenerate: o.SkipGenerate || o.Resume,
		KeepGoing:    o.KeepGoing,
		SkipDaily:    o.SkipDaily,
		RetryDelay:   time.Duration(o.RetryMinutes) * time.Minute,
		MaxAttempts:  o.MaxAttempts,
		OnState:      saveE2EState,
	}
	if o.Resume {
		st := loadE2EState()
		if st == nil {
			return fmt.Errorf("재개할 저장 상태가 없습니다")
		}
		opt.Resume = st
	}

	if err := a.setBusy("e2e"); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.mu.Lock()
	a.cancelE2E = cancel
	a.mu.Unlock()

	go func() {
		defer func() {
			a.mu.Lock()
			a.cancelE2E = nil
			a.mu.Unlock()
			a.clearBusy()
		}()
		res, err := e2e.Run(ctx, p, s, opt, func(pr e2e.Progress) {
			wruntime.EventsEmit(a.ctx, "e2e:progress", pr)
		})
		if res != nil {
			a.mu.Lock()
			a.lastE2E = res
			if res.Run != nil {
				a.lastRun = res.Run
			}
			if res.FinalVerify != nil {
				a.lastVerify = res.FinalVerify
			}
			a.mu.Unlock()

			if path, serr := autoSaveE2EReport(res); serr == nil {
				wruntime.EventsEmit(a.ctx, "e2e:progress",
					e2e.Progress{Phase: "verify", Message: "리포트 자동 저장: " + path})
			}
			// 미검증(INCONCLUSIVE)으로 끝난 실행은 상태를 지우지 않는다 —
			// 남은 셀을 다시 판정할 여지를 없애면 안 된다.
			if err == nil && res.Aborted == "" && !res.Inconclusive {
				discardE2EState() // 완주했으면 재개용 상태 정리
			}
		}
		if err != nil {
			notify("rawgen E2E 오류", err.Error())
			wruntime.EventsEmit(a.ctx, "e2e:error", err.Error())
			return
		}
		verdict := e2e.Verdict(res)
		if res.Aborted != "" {
			verdict = "중단됨"
		}
		notify("rawgen E2E "+verdict, fmt.Sprintf("verify %d회 실행 · 리포트 자동 저장됨", res.VerifyRuns))
		wruntime.WindowShow(a.ctx)
		wruntime.EventsEmit(a.ctx, "e2e:done", res)
	}()
	return nil
}

func (a *App) CancelE2E() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancelE2E != nil {
		a.cancelE2E()
	}
}

// E2EReport는 마지막 E2E 결과의 Markdown 리포트를 돌려준다.
func (a *App) E2EReport() (string, error) {
	a.mu.Lock()
	r := a.lastE2E
	a.mu.Unlock()
	if r == nil {
		return "", fmt.Errorf("E2E 실행 결과가 없습니다")
	}
	return e2e.Markdown(r), nil
}

// ---------- Report ----------

type ReportOut struct {
	Markdown string `json:"markdown"`
	JSON     string `json:"json"`
	CSV      string `json:"csv"`
}

func (a *App) BuildReport() (*ReportOut, error) {
	a.mu.Lock()
	b := report.Bundle{Preview: a.lastPreview, Run: a.lastRun, Verify: a.lastVerify, Version: appVersion}
	a.mu.Unlock()
	// 앱 재시작 후에도 최근 주입 리포트가 나오도록 영속 이력으로 폴백
	if b.Run == nil {
		if hist, err := executor.History(1); err == nil && len(hist) > 0 {
			b.Run = &hist[0]
		}
	}
	md := report.Markdown(b)
	js, err := report.JSON(b)
	if err != nil {
		return nil, err
	}
	out := &ReportOut{Markdown: md, JSON: js}
	if b.Verify != nil {
		csvText, err := report.CSV(b.Verify)
		if err == nil {
			out.CSV = csvText
		}
	}
	return out, nil
}

// ReportFromHistory는 실행 이력에서 특정 run의 리포트를 만든다.
// 앱을 껐다 켜도 과거 주입 기록을 문서화할 수 있다.
func (a *App) ReportFromHistory(runID string) (string, error) {
	hist, err := executor.History(0)
	if err != nil {
		return "", err
	}
	for i := range hist {
		if hist[i].RunID == runID {
			return report.Markdown(report.Bundle{Run: &hist[i], Version: appVersion}), nil
		}
	}
	return "", fmt.Errorf("이력에서 run_id %q를 찾지 못했습니다", runID)
}

// beforeClose는 창 닫기 직전 호출된다. 장기 실행 중이면 확인을 받는다.
// true 반환 = 닫기 방지.
func (a *App) beforeClose(ctx context.Context) bool {
	a.mu.Lock()
	busy := a.busy
	a.mu.Unlock()
	if busy == "" || busy == "verify" {
		return false
	}
	msg := fmt.Sprintf("%s 실행 중입니다. 종료하면 진행이 중단됩니다.", busyLabel(busy))
	if busy == "e2e" {
		msg += "\n(E2E는 진행 상태가 저장되므로 다음 실행 때 이어서 할 수 있습니다)"
	}
	// Windows는 QuestionDialog 버튼이 Yes/No 고정(커스텀 버튼은 macOS 전용)
	sel, err := wruntime.MessageDialog(ctx, wruntime.MessageDialogOptions{
		Type:    wruntime.QuestionDialog,
		Title:   "실행 중 종료",
		Message: msg + "\n\n종료할까요?",
	})
	if err != nil {
		return false
	}
	return sel != "Yes" && sel != "예"
}

// SaveReportFile은 저장 대화상자로 리포트를 파일로 저장한다.
func (a *App) SaveReportFile(content, defaultName string) (string, error) {
	path, err := wruntime.SaveFileDialog(a.ctx, wruntime.SaveDialogOptions{
		DefaultFilename: defaultName,
	})
	if err != nil || path == "" {
		return "", err
	}
	return path, os.WriteFile(path, []byte(content), 0o644)
}
