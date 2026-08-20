// Package profile은 DB 연결 프로파일을 로컬 파일(profiles.json)에 저장한다.
// secrets는 로컬 파일에만 있고, export 시에는 placeholder로 치환된다(공개 repo 정책).
package profile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type MariaDB struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	User     string `json:"user"`
	Password string `json:"password"`
}

type ClickHouse struct {
	Host     string `json:"host"`
	Port     int    `json:"port"` // native 프로토콜(기본 9000)
	Database string `json:"database"`
	User     string `json:"user"`
	Password string `json:"password"`
}

type Profile struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Maria    MariaDB    `json:"mariadb"`
	CH       ClickHouse `json:"clickhouse"`
	Timezone string     `json:"timezone"`
	// TestOnly=true인 프로파일에서만 INSERT를 허용한다(안전장치).
	TestOnly bool `json:"testOnly"`
	// 테이블명 오버라이드(기본값은 제품 표준명)
	CheckvalueTable   string `json:"checkvalueTable"`
	DailyStatsCHTable string `json:"dailyStatsChTable"`
	DailyStatsTable   string `json:"dailyStatsTable"`
	HourlyTable       string `json:"hourlyTable"`
	CheckpointTable   string `json:"checkpointTable"`
	ExcludeDateTable  string `json:"excludeDateTable"`

	// 세션 목록 정렬·표시용 기록(RFC3339). 값이 없어도 동작에는 영향이 없다.
	CreatedAt  string `json:"createdAt,omitempty"`
	UpdatedAt  string `json:"updatedAt,omitempty"`
	LastUsedAt string `json:"lastUsedAt,omitempty"`
}

func (p *Profile) ApplyDefaults() {
	if p.Timezone == "" {
		p.Timezone = "Asia/Seoul"
	}
	if p.Maria.Port == 0 {
		p.Maria.Port = 3306
	}
	if p.CH.Port == 0 {
		p.CH.Port = 9000
	}
	if p.CheckvalueTable == "" {
		p.CheckvalueTable = "checkvalue"
	}
	if p.DailyStatsCHTable == "" {
		p.DailyStatsCHTable = "daily_stats"
	}
	if p.DailyStatsTable == "" {
		p.DailyStatsTable = "daily_stats"
	}
	if p.HourlyTable == "" {
		p.HourlyTable = "hourly_checkpoint_data_log"
	}
	if p.CheckpointTable == "" {
		p.CheckpointTable = "checkpoint"
	}
	if p.ExcludeDateTable == "" {
		p.ExcludeDateTable = "exclude_date"
	}
}

type Store struct {
	mu   sync.Mutex
	path string
}

// legacyDirName은 개명 전(statsgenerator) 설정 디렉터리다.
// 프로파일(비밀번호 포함)·실행 이력·E2E 상태가 여기 있으므로 최초 1회 이관한다.
const legacyDirName = "statsgenerator"

// Dir은 설정 디렉터리를 반환한다(%APPDATA%\rawgen).
// 신규 폴더가 없고 구 폴더가 있으면 통째로 옮긴다(개명 이관, 실패 시 신규 폴더로 진행).
func Dir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "rawgen")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		legacy := filepath.Join(base, legacyDirName)
		if fi, lerr := os.Stat(legacy); lerr == nil && fi.IsDir() {
			_ = os.Rename(legacy, dir)
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// NewStoreAt은 임의 경로의 저장소를 연다(테스트용).
func NewStoreAt(path string) *Store { return &Store{path: path} }

func NewStore() (*Store, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	return &Store{path: filepath.Join(dir, "profiles.json")}, nil
}

func (s *Store) List() ([]Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

func (s *Store) load() ([]Profile, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return []Profile{}, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Profile
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("profiles.json 파싱 실패: %w", err)
	}
	for i := range out {
		out[i].ApplyDefaults()
	}
	return out, nil
}

// save는 임시 파일에 쓰고 rename한다. 세션이 여러 개가 되면 파일 하나에 전부 들어
// 있으므로, 쓰는 도중 앱이 죽으면 반쪽 JSON이 남아 전 세션이 통째로 사라진다.
func (s *Store) save(list []Profile) error {
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) Upsert(p Profile) (Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.Name == "" {
		return p, errors.New("세션 이름이 필요합니다")
	}
	p.ApplyDefaults()
	list, err := s.load()
	if err != nil {
		return p, err
	}
	now := time.Now().Format(time.RFC3339)
	p.UpdatedAt = now
	if p.ID == "" {
		p.ID = nextID(list)
		if p.CreatedAt == "" {
			p.CreatedAt = now
		}
		list = append(list, p)
	} else {
		found := false
		for i := range list {
			if list[i].ID == p.ID {
				if p.CreatedAt == "" {
					p.CreatedAt = list[i].CreatedAt
				}
				if p.LastUsedAt == "" {
					p.LastUsedAt = list[i].LastUsedAt
				}
				list[i] = p
				found = true
				break
			}
		}
		if !found {
			if p.CreatedAt == "" {
				p.CreatedAt = now
			}
			list = append(list, p)
		}
	}
	return p, s.save(list)
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.load()
	if err != nil {
		return err
	}
	out := list[:0]
	for _, p := range list {
		if p.ID != id {
			out = append(out, p)
		}
	}
	return s.save(out)
}

func (s *Store) Get(id string) (Profile, error) {
	list, err := s.List()
	if err != nil {
		return Profile{}, err
	}
	for _, p := range list {
		if p.ID == id || p.Name == id {
			return p, nil
		}
	}
	return Profile{}, fmt.Errorf("세션 %q을(를) 찾을 수 없습니다", id)
}

func hasID(list []Profile, id string) bool {
	for _, p := range list {
		if p.ID == id {
			return true
		}
	}
	return false
}

// Export는 secret을 placeholder로 치환한 JSON을 반환한다(공유/공개 repo용).
func (s *Store) Export() (string, error) {
	list, err := s.List()
	if err != nil {
		return "", err
	}
	for i := range list {
		if list[i].Maria.Password != "" {
			list[i].Maria.Password = "<MARIADB_PASSWORD>"
		}
		if list[i].CH.Password != "" {
			list[i].CH.Password = "<CLICKHOUSE_PASSWORD>"
		}
	}
	data, err := json.MarshalIndent(list, "", "  ")
	return string(data), err
}

// nextID는 지금 쓰이는 가장 큰 번호 다음을 준다. 빈 번호를 재사용하면
// 세션을 지우고 새로 만들었을 때 새 세션이 남의 ID를 물려받는다
// (진행 중 E2E 상태·UI 선택이 ID로 세션을 가리킨다).
func nextID(list []Profile) string {
	max := 0
	for _, p := range list {
		var n int
		if _, err := fmt.Sscanf(p.ID, "p%d", &n); err == nil && n > max {
			max = n
		}
	}
	for n := max + 1; ; n++ {
		id := fmt.Sprintf("p%d", n)
		if !hasID(list, id) {
			return id
		}
	}
}

// Touch는 세션을 마지막으로 사용한 시각을 기록한다(목록 정렬·표시용).
// 실패해도 작업을 막지 않는다 — 부가 정보다.
func (s *Store) Touch(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.load()
	if err != nil {
		return
	}
	for i := range list {
		if list[i].ID == id {
			list[i].LastUsedAt = time.Now().Format(time.RFC3339)
			_ = s.save(list)
			return
		}
	}
}

// Duplicate는 세션을 통째로 복제한다. 같은 서버에 테이블명만 다른 배포본을
// 등록하거나, 공유받은 세션을 원본 보존한 채 고칠 때 쓴다(비밀번호도 함께 복제).
func (s *Store) Duplicate(id string) (Profile, error) {
	src, err := s.Get(id)
	if err != nil {
		return Profile{}, err
	}
	s.mu.Lock()
	list, lerr := s.load()
	s.mu.Unlock()
	if lerr != nil {
		return Profile{}, lerr
	}
	src.ID = ""
	src.CreatedAt = ""
	src.LastUsedAt = ""
	src.Name = uniqueName(list, src.Name+" 복사")
	return s.Upsert(src)
}

func uniqueName(list []Profile, base string) string {
	name := base
	for n := 2; hasName(list, name); n++ {
		name = fmt.Sprintf("%s %d", base, n)
	}
	return name
}

func hasName(list []Profile, name string) bool {
	for _, p := range list {
		if p.Name == name {
			return true
		}
	}
	return false
}

// ImportSummary는 가져오기 결과다. 무엇이 새로 생겼고 무엇을 덮었는지,
// 비밀번호가 빠진 세션이 어느 것인지 화면에서 그대로 보여주기 위한 값이다.
type ImportSummary struct {
	Added    []string `json:"added"`
	Updated  []string `json:"updated"`
	Warnings []string `json:"warnings"`
}

// 내보내기가 비밀번호를 치환하는 placeholder. 가져올 때 이 문자열이 그대로 오면
// 비밀번호가 아니라 "빠진 자리"이므로 절대 저장하지 않는다.
const (
	phMaria = "<MARIADB_PASSWORD>"
	phCH    = "<CLICKHOUSE_PASSWORD>"
)

// Import는 내보낸 JSON을 읽어 세션을 등록한다. 이름이 같으면 그 세션을 갱신하고,
// 없으면 새로 만든다. 배열/단일 객체/{"profiles":[...]} 세 형태를 모두 받는다.
func (s *Store) Import(text string) (ImportSummary, error) {
	var sum ImportSummary
	incoming, err := parseProfilesJSON(text)
	if err != nil {
		return sum, err
	}
	if len(incoming) == 0 {
		return sum, errors.New("가져올 세션이 없습니다")
	}
	for _, in := range incoming {
		if strings.TrimSpace(in.Name) == "" {
			sum.Warnings = append(sum.Warnings, "이름 없는 항목을 건너뛰었습니다")
			continue
		}
		existing, findErr := s.Get(in.Name)
		in.ID = ""
		if findErr == nil {
			in.ID = existing.ID
			in.CreatedAt = existing.CreatedAt
			in.LastUsedAt = existing.LastUsedAt
		}
		// placeholder는 비밀번호가 아니다. 기존 값이 있으면 유지하고, 없으면 비운다.
		if in.Maria.Password == phMaria {
			in.Maria.Password = existing.Maria.Password
			if in.Maria.Password == "" {
				sum.Warnings = append(sum.Warnings, in.Name+": MariaDB 비밀번호가 빠져 있습니다 — 직접 입력하세요")
			}
		}
		if in.CH.Password == phCH {
			in.CH.Password = existing.CH.Password
			if in.CH.Password == "" {
				sum.Warnings = append(sum.Warnings, in.Name+": ClickHouse 비밀번호가 빠져 있습니다 — 직접 입력하세요")
			}
		}
		// 남의 환경에서 TestOnly가 켜진 채로 넘어오면 그 세션에 INSERT가 열린다.
		// 가져온 세션은 항상 꺼진 상태로 시작하고, 사람이 직접 확인해서 켜야 한다.
		if in.TestOnly {
			sum.Warnings = append(sum.Warnings, in.Name+": TestOnly를 껐습니다 — 테스트 DB임을 확인한 뒤 직접 켜세요")
			in.TestOnly = false
		}
		if _, err := s.Upsert(in); err != nil {
			return sum, err
		}
		if findErr == nil {
			sum.Updated = append(sum.Updated, in.Name)
		} else {
			sum.Added = append(sum.Added, in.Name)
		}
	}
	return sum, nil
}

func parseProfilesJSON(text string) ([]Profile, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, errors.New("내용이 비어 있습니다")
	}
	var list []Profile
	if err := json.Unmarshal([]byte(trimmed), &list); err == nil {
		return list, nil
	}
	var wrapped struct {
		Profiles []Profile `json:"profiles"`
	}
	if err := json.Unmarshal([]byte(trimmed), &wrapped); err == nil && len(wrapped.Profiles) > 0 {
		return wrapped.Profiles, nil
	}
	var one Profile
	if err := json.Unmarshal([]byte(trimmed), &one); err == nil && one.Name != "" {
		return []Profile{one}, nil
	}
	return nil, errors.New("세션 JSON으로 해석할 수 없습니다 (배열 또는 {\"profiles\":[...]} 형태)")
}
