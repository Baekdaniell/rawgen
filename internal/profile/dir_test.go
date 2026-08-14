package profile

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// 개명(statsgenerator → rawgen) 시 설정 폴더가 통째로 이관되는지 확인한다.
// 프로파일(비밀번호)·실행 이력·E2E 상태가 걸려 있어 유실되면 안 된다.
func TestDirMigratesLegacyFolder(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("os.UserConfigDir 기준이 플랫폼마다 달라 Windows에서만 검증")
	}
	base := t.TempDir()
	t.Setenv("AppData", base)

	legacy := filepath.Join(base, legacyDirName)
	if err := os.MkdirAll(filepath.Join(legacy, "reports"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "profiles.json"), []byte(`[{"id":"x"}]`), 0o600); err != nil {
		t.Fatal(err)
	}

	dir, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(base, "rawgen"); dir != want {
		t.Fatalf("Dir() = %s, want %s", dir, want)
	}
	data, err := os.ReadFile(filepath.Join(dir, "profiles.json"))
	if err != nil {
		t.Fatalf("이관 후 profiles.json을 읽지 못함: %v", err)
	}
	if string(data) != `[{"id":"x"}]` {
		t.Errorf("profiles.json 내용이 바뀜: %s", data)
	}
	if _, err := os.Stat(filepath.Join(dir, "reports")); err != nil {
		t.Errorf("하위 폴더(reports)가 함께 이관되지 않음: %v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("구 폴더가 남아 있음(이동이 아니라 복사?): %v", err)
	}
}

// 신규 폴더가 이미 있으면 구 폴더를 건드리지 않아야 한다(덮어쓰기 사고 방지).
func TestDirKeepsExistingWhenBothPresent(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows 전용")
	}
	base := t.TempDir()
	t.Setenv("AppData", base)

	current := filepath.Join(base, "rawgen")
	if err := os.MkdirAll(current, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(current, "profiles.json"), []byte(`new`), 0o600); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(base, legacyDirName)
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "profiles.json"), []byte(`old`), 0o600); err != nil {
		t.Fatal(err)
	}

	dir, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "profiles.json"))
	if string(data) != "new" {
		t.Errorf("기존 폴더가 구 폴더로 덮어써짐: %s", data)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Errorf("구 폴더가 사라짐(보존해야 함): %v", err)
	}
}
