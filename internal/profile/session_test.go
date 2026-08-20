package profile

import (
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStoreAt(filepath.Join(t.TempDir(), "profiles.json"))
}

func mk(name, chHost string) Profile {
	return Profile{Name: name, CH: ClickHouse{Host: chHost}, Maria: MariaDB{Host: chHost}}
}

// 환경마다 세션을 따로 등록해 쓴다. 이름이 달라도 서로 덮어쓰면 안 되고,
// ID는 이력·E2E 상태가 가리키는 값이라 재사용되면 안 된다.
func TestUpsertKeepsSessionsSeparate(t *testing.T) {
	s := newTestStore(t)
	a, err := s.Upsert(mk("qa-koscom", "10.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Upsert(mk("qa-local", "127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID {
		t.Fatalf("두 세션이 같은 ID를 받았다: %s", a.ID)
	}
	list, _ := s.List()
	if len(list) != 2 {
		t.Fatalf("세션이 %d개다 (2개여야 한다)", len(list))
	}

	// 가운데를 지우고 새로 만들어도 지워진 ID를 물려주지 않아야 한다.
	if err := s.Delete(a.ID); err != nil {
		t.Fatal(err)
	}
	c, _ := s.Upsert(mk("qa-skbb", "10.0.0.2"))
	if c.ID == a.ID {
		t.Fatalf("삭제된 세션의 ID(%s)를 재사용했다", a.ID)
	}
	if c.CreatedAt == "" || c.UpdatedAt == "" {
		t.Fatal("생성/수정 시각이 기록되지 않았다")
	}
}

func TestDuplicateMakesIndependentCopy(t *testing.T) {
	s := newTestStore(t)
	src := mk("qa-koscom", "10.0.0.1")
	src.CH.Password = "secret"
	src.HourlyTable = "hourly_custom"
	saved, _ := s.Upsert(src)

	copy1, err := s.Duplicate(saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if copy1.ID == saved.ID {
		t.Fatal("복제본이 원본과 같은 ID다 — 저장하면 원본이 덮인다")
	}
	if copy1.CH.Password != "secret" || copy1.HourlyTable != "hourly_custom" {
		t.Fatalf("복제본이 설정을 잃었다: %+v", copy1)
	}
	copy2, _ := s.Duplicate(saved.ID)
	if copy2.Name == copy1.Name {
		t.Fatalf("복제본 이름이 겹친다: %s", copy2.Name)
	}
}

// 공유받은 JSON을 가져올 때: 이름이 같으면 갱신, 없으면 추가.
// 비밀번호 자리표시자와 TestOnly는 절대 그대로 들어오면 안 된다.
func TestImportMergesByNameAndDropsSecrets(t *testing.T) {
	s := newTestStore(t)
	existing := mk("qa-koscom", "10.0.0.1")
	existing.Maria.Password = "mine"
	existing.CH.Password = "mine-ch"
	if _, err := s.Upsert(existing); err != nil {
		t.Fatal(err)
	}

	in := `[
	  {"name":"qa-koscom","clickhouse":{"host":"10.0.0.9","password":"<CLICKHOUSE_PASSWORD>"},
	   "mariadb":{"host":"10.0.0.9","password":"<MARIADB_PASSWORD>"},"testOnly":true},
	  {"name":"qa-new","clickhouse":{"host":"10.0.0.8"},"mariadb":{"host":"10.0.0.8"},"testOnly":true}
	]`
	sum, err := s.Import(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Updated) != 1 || len(sum.Added) != 1 {
		t.Fatalf("추가/갱신 집계가 틀렸다: %+v", sum)
	}

	got, err := s.Get("qa-koscom")
	if err != nil {
		t.Fatal(err)
	}
	if got.CH.Host != "10.0.0.9" {
		t.Fatalf("갱신이 반영되지 않았다: %s", got.CH.Host)
	}
	if got.Maria.Password != "mine" || got.CH.Password != "mine-ch" {
		t.Fatalf("자리표시자가 기존 비밀번호를 덮었다: %+v", got)
	}
	if got.TestOnly {
		t.Fatal("가져온 세션에서 TestOnly가 켜진 채로 남았다 — 남의 환경에 INSERT가 열린다")
	}
	fresh, _ := s.Get("qa-new")
	if fresh.TestOnly {
		t.Fatal("새로 가져온 세션의 TestOnly가 켜져 있다")
	}
	if fresh.Maria.Port != 3306 || fresh.CheckvalueTable != "checkvalue" {
		t.Fatalf("기본값이 채워지지 않았다: %+v", fresh)
	}
}

func TestImportAcceptsWrappedAndRejectsGarbage(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Import(`{"profiles":[{"name":"w1","clickhouse":{"host":"h"}}]}`); err != nil {
		t.Fatalf("{\"profiles\":[...]} 형태를 거부했다: %v", err)
	}
	if _, err := s.Get("w1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Import("not json"); err == nil {
		t.Fatal("JSON이 아닌 입력을 받아들였다")
	}
	if _, err := s.Import("   "); err == nil {
		t.Fatal("빈 입력을 받아들였다")
	}
}

func TestTouchRecordsLastUsed(t *testing.T) {
	s := newTestStore(t)
	p, _ := s.Upsert(mk("qa", "h"))
	s.Touch(p.ID)
	got, _ := s.Get(p.ID)
	if got.LastUsedAt == "" {
		t.Fatal("마지막 사용 시각이 기록되지 않았다")
	}
	// 저장 시 LastUsedAt이 폼 값(빈 값)으로 지워지면 목록 표시가 리셋된다.
	got.Name = "qa2"
	got.LastUsedAt = ""
	if _, err := s.Upsert(got); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Get(p.ID)
	if after.LastUsedAt == "" {
		t.Fatal("세션을 저장하자 마지막 사용 기록이 지워졌다")
	}
}
