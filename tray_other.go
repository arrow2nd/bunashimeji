//go:build !windows

package main

// 非 Windows ビルド向けの no-op スタブ。tray は Windows のみ対応 (platform 全体が
// Windows 限定なので、tray もそれに合わせる)。

type TrayCallbacks struct {
	OnSpawnRandom func()
	OnGather      func()
	OnKeepOne     func()
	OnQuit        func()
}

func startTray(cb TrayCallbacks) {}

func stopTray() {}
