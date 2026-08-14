//go:build windows

package main

import (
	"runtime"
	"syscall"
)

const (
	esContinuous     = 0x80000000
	esSystemRequired = 0x00000001
	// ES_DISPLAY_REQUIRED는 일부러 제외 — 화면은 꺼져도 되고 시스템 절전만 막는다
)

var setThreadExecutionState = syscall.NewLazyDLL("kernel32.dll").NewProc("SetThreadExecutionState")

// startKeepAwake는 장기 실행(E2E·Generate) 동안 시스템 절전을 막는다.
// 절전에 들어가면 마일스톤 대기 타이머가 통째로 밀려 시간대별 판정
// 시점(보존 창 당일+전일 23시)을 놓친다.
// SetThreadExecutionState는 호출 스레드 기준이므로 OS 스레드에 고정한
// 전용 고루틴이 잡고, stop()이 호출되면 해제 후 종료한다.
func startKeepAwake() func() {
	done := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		setThreadExecutionState.Call(uintptr(esContinuous | esSystemRequired))
		<-done
		setThreadExecutionState.Call(uintptr(esContinuous))
	}()
	var once bool
	return func() {
		if !once {
			once = true
			close(done)
		}
	}
}
