package mariadb

import (
	"testing"
	"time"
)

// max_time 컬럼 타입은 배포본마다 다르다. epoch만 파싱하고 실패를 nil로 남기면
// 대조가 조용히 사라지므로, DATETIME 배포본도 해석되어야 한다.
func TestParseMaxTime(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Seoul")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 8, 14, 17, 59, 59, 0, loc).UnixMilli()

	cases := []struct {
		name string
		in   string
		want *int64
	}{
		{"epoch ms", "1786000000000", ptr(int64(1786000000000))},
		{"datetime", "2026-08-14 17:59:59", &want},
		{"datetime T", "2026-08-14T17:59:59", &want},
		{"빈 값", "", nil},
		{"NULL 문자열", "NULL", nil},
		{"해석 불가", "0000-00-00 00:00:00", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseMaxTime(c.in, loc)
			switch {
			case c.want == nil && got != nil:
				t.Errorf("got %d, want nil", *got)
			case c.want != nil && got == nil:
				t.Errorf("got nil, want %d", *c.want)
			case c.want != nil && *got != *c.want:
				t.Errorf("got %d, want %d", *got, *c.want)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }
