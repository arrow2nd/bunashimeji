package main

import (
	"bytes"
	"encoding/binary"
	_ "embed"
	"fmt"
	"image/png"
	"log"
	"runtime"

	"fyne.io/systray"

	"github.com/arrow2nd/bunashimeji/platform"
)

// ルートの buna.png を tray アイコンに使う (exe アイコンと同一画像)。Windows tray は
// ICO バイト列を要求するため pngToICO で最小 ICO ヘッダ (PNG-in-ICO, Vista+) を被せて渡す。
//
//go:embed buna.png
var trayIconPNG []byte

// TrayCallbacks は tray メニュー各項目に対応するハンドラ束。
// 呼び出し元 (main.go) で spawner や cancel をクロージャに包んで渡す。
// すべて非 nil 前提 (nil チェックは入れない)。
type TrayCallbacks struct {
	OnSpawnRandom   func()             // 「ふやす」: ランダムでキャラを 1 体追加
	OnSummon        func(name string)  // 「呼ぶ」サブメニュー: 指定キャラを 1 体追加
	OnGather        func()             // 「あつまれ！」: 全キャラをカーソルへ集合
	OnKeepOne       func()             // 「1匹だけのこす」: 1 体だけ残して他を消す
	OnQuit          func()             // 「ばいばい」: アプリ終了
	CharacterNames  func() []string    // 「呼ぶ」サブメニューの列挙ソース (img/ 直下のキャラ一覧)
}

// startTray は別 goroutine で systray を起動する。menu クリックは cb の各ハンドラへ転送する。
// systray の Win32 hidden window は生成スレッドで GetMessage を回す必要があるため、
// goroutine 冒頭で LockOSThread して OS スレッドに固定する。
func startTray(cb TrayCallbacks) {
	go func() {
		runtime.LockOSThread()
		systray.Run(func() {
			systray.SetTitle("ぶなしめじ")
			systray.SetTooltip("ぶなしめじ")
			if ico, err := pngToICO(trayIconPNG); err != nil {
				log.Printf("tray icon: %v", err)
			} else {
				systray.SetIcon(ico)
			}
			mSpawn := systray.AddMenuItem("ふやす", "ランダムでキャラを1体増やす")

			// 「呼ぶ」サブメニュー: img/ 配下のキャラを指名召喚する。起動時に固定リストを
			// 生成するため、後から img/ にキャラを足したい場合は再起動が必要。
			mSummon := systray.AddMenuItem("呼ぶ", "指定したキャラを1体増やす")
			buildSummonSubmenu(mSummon, cb)

			mGather := systray.AddMenuItem("あつまれ！", "全キャラをマウスカーソルの近くに集める")
			mKeep := systray.AddMenuItem("1匹だけのこす", "1体だけ残して他を消す")

			// 「反応するウィンドウ」サブメニュー: プリセット + windows.json のユーザ追加分を
			// チェックボックスで列挙する。toggle は platform 側で config 保存 + 再適用まで連動。
			mWindows := systray.AddMenuItem("反応するウィンドウ", "投げて遊べるアプリ")
			buildWhitelistSubmenu(mWindows)

			systray.AddSeparator()
			mQuit := systray.AddMenuItem("ばいばい", "ぶなしめじを終了")
			go func() {
				for {
					select {
					case <-mSpawn.ClickedCh:
						cb.OnSpawnRandom()
					case <-mGather.ClickedCh:
						cb.OnGather()
					case <-mKeep.ClickedCh:
						cb.OnKeepOne()
					case <-mQuit.ClickedCh:
						cb.OnQuit()
						systray.Quit()
						return
					}
				}
			}()
		}, func() {
			// tray が閉じられた → アプリ終了経路へ
			cb.OnQuit()
		})
	}()
}

// stopTray は main 側の終了に追随して systray を畳む。多重呼び出しは systray 側で吸収される。
func stopTray() {
	systray.Quit()
}

// buildSummonSubmenu は parent の下に img/ 直下のキャラ名を leaf として並べ、
// 各項目のクリックを listener goroutine で待ち受けて cb.OnSummon に転送する。
// CharacterNames() が空かエラーで空配列を返した場合は親項目を Disable する。
func buildSummonSubmenu(parent *systray.MenuItem, cb TrayCallbacks) {
	names := cb.CharacterNames()
	if len(names) == 0 {
		parent.Disable()
		return
	}
	for _, name := range names {
		item := parent.AddSubMenuItem(name, name)
		go summonItemLoop(item, name, cb)
	}
}

// summonItemLoop は 1 つの sub-menu item のクリックを永続的に待ち受け、
// 押されるたびに cb.OnSummon(name) を呼ぶ。クローズ条件は systray.Quit でプロセスが
// 落ちるとき (= goroutine ごと消える)。
func summonItemLoop(item *systray.MenuItem, name string, cb TrayCallbacks) {
	for range item.ClickedCh {
		cb.OnSummon(name)
	}
}

// buildWhitelistSubmenu は parent の下にプリセット + ユーザ追加分のチェックボックス項目を並べ、
// 各項目のクリックを listener goroutine で待ち受ける。
//
// systray は AddSubMenuItemCheckbox 後にエントリを動的に Remove/Hide できるが、
// 本 MVP では「起動時に固定リストを生成、toggle は ON/OFF 状態だけ変える」方針。
// JSON にエントリを追加したい場合は再起動が必要。
func buildWhitelistSubmenu(parent *systray.MenuItem) {
	entries := platform.EffectiveEntries()
	if len(entries) == 0 {
		// プリセットも user 追加も無い → サブメニューを無効化
		parent.Disable()
		return
	}

	// プリセットとユーザ追加分の境目に区切り線を入れたいが、systray のサブメニューに
	// セパレータを足す API が無いため、user 分の先頭ラベルに記号で示すだけに留める。
	userBoundaryShown := false
	for _, e := range entries {
		label := e.Label
		if !e.IsPreset && !userBoundaryShown {
			label = "─ " + label // ユーザ追加分の先頭マーカー
			userBoundaryShown = true
		}
		item := parent.AddSubMenuItemCheckbox(label, tooltipFor(e), e.Enabled)
		go whitelistItemLoop(item, e)
	}
}

// whitelistItemLoop は 1 つの sub-menu item のクリックを永続的に待ち受ける。
// クリックごとに表示チェック状態を反転して platform へ保存・反映を委ねる。
//
// item.ClickedCh が close されることは無いので無限ループで OK
// (systray.Quit 時はプロセス終了で goroutine ごと消える)。
func whitelistItemLoop(item *systray.MenuItem, e platform.WhitelistEntry) {
	for range item.ClickedCh {
		newState := !item.Checked()
		if newState {
			item.Check()
		} else {
			item.Uncheck()
		}
		var err error
		if e.IsPreset {
			err = platform.TogglePreset(e.PresetID, newState)
		} else {
			err = platform.ToggleUserEntry(e.UserIdx, newState)
		}
		if err != nil {
			log.Printf("tray: toggle %q: %v", e.Label, err)
		}
	}
}

// tooltipFor は WhitelistEntry のマッチ条件を短く tooltip 文字列にする。
// hover 時にユーザが「何にマッチするか」を確認できるよう、最も特徴的なフィールド 1 つを出す。
func tooltipFor(e platform.WhitelistEntry) string {
	switch {
	case e.Matcher.ExeEquals != "":
		return "exe: " + e.Matcher.ExeEquals
	case e.Matcher.ClassContains != "" && e.Matcher.TitleContains != "":
		return "class: " + e.Matcher.ClassContains + " / title: " + e.Matcher.TitleContains
	case e.Matcher.ClassContains != "":
		return "class: " + e.Matcher.ClassContains
	case e.Matcher.TitleContains != "":
		return "title: " + e.Matcher.TitleContains
	}
	return ""
}

// pngToICO は PNG バイト列を最小 ICO コンテナに包む (PNG-in-ICO, Vista 以降が対応)。
// fyne.io/systray@Windows は SetIcon に .ico バイト列を要求し、tmp ファイル経由で
// LoadImage(LR_LOADFROMFILE) するため、PNG をそのまま渡せない。
// 外部依存・CGo なしで shime1.png を tray アイコン化するための薄いラッパ。
func pngToICO(pngBytes []byte) ([]byte, error) {
	cfg, err := png.DecodeConfig(bytes.NewReader(pngBytes))
	if err != nil {
		return nil, fmt.Errorf("decode png: %w", err)
	}
	// ICONDIRENTRY の Width/Height は 1 byte。256 以上は 0 で表現。
	w := byte(cfg.Width)
	if cfg.Width >= 256 {
		w = 0
	}
	h := byte(cfg.Height)
	if cfg.Height >= 256 {
		h = 0
	}
	const (
		iconDirSize   = 6
		iconEntrySize = 16
		dataOffset    = iconDirSize + iconEntrySize
	)
	var buf bytes.Buffer
	// ICONDIR
	_ = binary.Write(&buf, binary.LittleEndian, uint16(0)) // Reserved
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1)) // Type = 1 (icon)
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1)) // Count
	// ICONDIRENTRY
	buf.WriteByte(w)
	buf.WriteByte(h)
	buf.WriteByte(0) // ColorCount (>=8bpp は 0)
	buf.WriteByte(0) // Reserved
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1))                // Planes
	_ = binary.Write(&buf, binary.LittleEndian, uint16(32))               // BitCount
	_ = binary.Write(&buf, binary.LittleEndian, uint32(len(pngBytes)))    // SizeBytes
	_ = binary.Write(&buf, binary.LittleEndian, uint32(dataOffset))       // Offset
	buf.Write(pngBytes)
	return buf.Bytes(), nil
}
