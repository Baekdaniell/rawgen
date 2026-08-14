//go:build windows

package main

import toast "git.sr.ht/~jackmordaunt/go-toast/v2"

// notify는 Windows 토스트 알림을 보낸다. 온종일 E2E가 끝났을 때 앱을
// 최소화해 둔 사용자에게 알리는 용도라 실패는 조용히 무시한다.
func notify(title, body string) {
	n := toast.Notification{AppID: "rawgen", Title: title, Body: body}
	_ = n.Push()
}
