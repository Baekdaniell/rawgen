// Package profile은 DB 연결 프로파일을 로컬 파일(profiles.json)에 저장한다.
// secrets는 로컬 파일에만 있고, export 시에는 placeholder로 치환된다(공개 repo 정책).
package profile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
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

func (s *Store) save(list []Profile) error {
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}

func (s *Store) Upsert(p Profile) (Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.Name == "" {
		return p, errors.New("프로파일 이름이 필요합니다")
	}
	p.ApplyDefaults()
	list, err := s.load()
	if err != nil {
		return p, err
	}
	if p.ID == "" {
		p.ID = fmt.Sprintf("p%d", len(list)+1)
		for hasID(list, p.ID) {
			p.ID += "x"
		}
		list = append(list, p)
	} else {
		found := false
		for i := range list {
			if list[i].ID == p.ID {
				list[i] = p
				found = true
				break
			}
		}
		if !found {
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
	return Profile{}, fmt.Errorf("프로파일 %q을(를) 찾을 수 없습니다", id)
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
