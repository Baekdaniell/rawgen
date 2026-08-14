# rawgen (OQT-325)

MK119 통계 검증용 과거 raw data 생성기. 목표 min/max/avg를 만족하는 checkvalue 데이터를
과거 날짜로 ClickHouse에 주입하고, 제품이 MariaDB에 저장한 daily/hourly 통계를
기대값과 자동 비교(L1/L2)한다. hourly는 "온종일 E2E" 모드로 주입→시간대별 자동
verify→리포트를 한 번에 수행한다.

사용 방법은 [사용설명서.md](사용설명서.md) 참고 (초보자용, 한국어).
설계 근거는 개발 계획서 v0.2 (Obsidian Vault\mk119) 참고.

## 구성

- `build/bin/rawgen.exe` — GUI 단일 실행 파일 (Wails v2 + WebView2). 모든 작업이 GUI에서 가능하다 (CLI는 2026-08-14 폐기, 기능 전부 GUI로 이식)
- `internal/` — model, generator, planner, profile, chdb, mariadb, executor, verify, e2e, report

## Build

```powershell
$env:PATH = "C:\Program Files\Go\bin;$env:PATH"
& "$env:USERPROFILE\go\bin\wails.exe" build     # GUI (frontend 포함 전체 빌드)
go test ./internal/...                           # 단위 테스트
```

## 안전장치

- TestOnly 프로파일에서만 INSERT 허용, 기본은 dry-run
- 보존 창(14일) 밖 날짜 실행 차단
- INSERT 직후 readback(건수 + 중복 카나리) 고정 수행
- Verify 전 복제 지연 가드(60초)
- E2E 시작 전 사전 점검(연결·TestOnly·오늘 날짜·셀 계획) — 과거 날짜 시나리오로 하루치 INSERT를 낭비하는 실수 차단
- Generate/E2E/Verify 상호 배타 — E2E 대기 중 추가 INSERT로 expected가 오염되는 것 방지
- E2E 진행 상태 영속화(매 판정 저장) + 리포트 자동 저장(%APPDATA%\rawgen\reports) + 실행 중 창 닫기 확인 + 절전 방지 + 완료 토스트 알림
- 프로파일 내보내기 시 비밀번호 placeholder 치환 (secrets는 %APPDATA%\rawgen 로컬 전용)
