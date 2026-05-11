//go:build windows

package platform

import (
	"errors"
	"image"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ActiveWindow は現在のフォアグラウンドウィンドウのうち、
// ホワイトリストに合致したもののジオメトリ。
//
// 本家の `mascot.environment.activeIE` に相当する。
// 元来 Internet Explorer 用だったため "IE" の名前が残っているが、
// 当方では任意のホワイトリスト対象ウィンドウを意味する。
//
// Visible=false の場合、Rect 等の他フィールドは無効値なので参照しない。
type ActiveWindow struct {
	Visible bool
	Rect    image.Rectangle // 仮想デスクトップ座標 (Win32 GetWindowRect 互換)

	// ID はウィンドウの安定識別子 (Win32 では HWND の uintptr 表現)。
	// tick 間で値が同じなら「同じウィンドウ」と判定できる。
	// 異なるウィンドウにフォーカスが切り替わったら値が変わる。
	ID uintptr

	ClassName string // デバッグ・ロギング用
	Title     string // デバッグ・ロギング用
}

// WindowMatcher はホワイトリスト 1 件分のマッチ条件。
// ClassContains と TitleContains は case-insensitive の部分一致。
// 両方指定なら AND マッチ、片方のみなら指定された方だけ評価。
// 両方とも空のエントリは無効 (誤って全マッチさせないため)。
type WindowMatcher struct {
	ClassContains string
	TitleContains string
}

// 自プロセスのマスコットウィンドウのクラス名 (window_windows.go の RegisterClassExW で
// 登録するもの)。フォアグラウンド検出時に除外する。
const ownWindowClassName = "BunashimejiWindow"

var (
	whitelistMu sync.RWMutex
	whitelist   = defaultWhitelist()
)

// defaultWhitelist は出荷時のホワイトリスト。
// よく使うウィンドウだけ最小限。ユーザは将来 conf/windows.json (仮) で
// 上書きできる予定。詳細は docs/window-whitelist.md 参照。
func defaultWhitelist() []WindowMatcher {
	return []WindowMatcher{
		// メモ帳 (動作確認用、最小・確実にマッチする)
		{ClassContains: "Notepad"},
		// Chromium ベース (Chrome / Edge / Brave / Opera 等)
		{ClassContains: "Chrome_WidgetWin_1"},
		// Firefox
		{ClassContains: "MozillaWindowClass"},
		// Windows Terminal
		{ClassContains: "CASCADIA_HOSTING_WINDOW_CLASS"},
		// 旧コンソール (cmd.exe 等)
		{ClassContains: "ConsoleWindowClass"},
		// VS Code (Code.exe; Chrome_WidgetWin_1 で既にカバー済みだが title でも明示)
		{TitleContains: "Visual Studio Code"},
	}
}

// SetWhitelist はホワイトリストを差し替える。
// 将来 config ファイルからロードする際のエントリポイント。
// nil または空スライスを渡すとホワイトリストが無効化され、ActiveWindow.Visible は常に false。
func SetWhitelist(matchers []WindowMatcher) {
	whitelistMu.Lock()
	defer whitelistMu.Unlock()
	whitelist = append([]WindowMatcher{}, matchers...)
}

// Whitelist は現在のホワイトリストのコピーを返す (デバッグ用)。
func Whitelist() []WindowMatcher {
	whitelistMu.RLock()
	defer whitelistMu.RUnlock()
	out := make([]WindowMatcher, len(whitelist))
	copy(out, whitelist)
	return out
}

var (
	procGetForegroundWindow  = user32.NewProc("GetForegroundWindow")
	procGetWindowRect        = user32.NewProc("GetWindowRect")
	procGetClassNameW        = user32.NewProc("GetClassNameW")
	procGetWindowTextW       = user32.NewProc("GetWindowTextW")
	procGetWindowTextLengthW = user32.NewProc("GetWindowTextLengthW")
	procIsWindowVisible      = user32.NewProc("IsWindowVisible")
	procIsIconic             = user32.NewProc("IsIconic")
	procIsZoomed             = user32.NewProc("IsZoomed")
	procIsWindow             = user32.NewProc("IsWindow")
)

// GetActiveWindow へのキャッシュ。
// 1 tick (40ms) のうちに N 体のマスコットが env.Refresh() するたびに syscall を打つのを避ける。
var (
	awCache     ActiveWindow
	awCacheTime time.Time
)

const awCacheTTL = 30 * time.Millisecond

// GetActiveWindow はフォアグラウンドウィンドウを取得し、ホワイトリスト判定後に返す。
// 30ms キャッシュ付きなので 1 tick 内の重複呼び出しは安全に高速化される。
func GetActiveWindow() ActiveWindow {
	if time.Since(awCacheTime) < awCacheTTL {
		return awCache
	}
	awCache = computeActiveWindow()
	awCacheTime = time.Now()
	return awCache
}

func computeActiveWindow() ActiveWindow {
	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 {
		return ActiveWindow{}
	}
	if visible, _, _ := procIsWindowVisible.Call(hwnd); visible == 0 {
		return ActiveWindow{}
	}
	if iconic, _, _ := procIsIconic.Call(hwnd); iconic != 0 {
		return ActiveWindow{} // 最小化中は見えないものとして扱う
	}

	className := getClassName(hwnd)
	if className == ownWindowClassName {
		return ActiveWindow{} // 自分のマスコットウィンドウは除外
	}
	title := getWindowText(hwnd)

	if !matchesWhitelist(className, title) {
		return ActiveWindow{}
	}

	var r rect
	ret, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	if ret == 0 {
		return ActiveWindow{}
	}
	return ActiveWindow{
		Visible:   true,
		Rect:      image.Rect(int(r.Left), int(r.Top), int(r.Right), int(r.Bottom)),
		ID:        hwnd,
		ClassName: className,
		Title:     title,
	}
}

func matchesWhitelist(className, title string) bool {
	whitelistMu.RLock()
	defer whitelistMu.RUnlock()
	cn := strings.ToLower(className)
	tt := strings.ToLower(title)
	for _, m := range whitelist {
		if m.ClassContains == "" && m.TitleContains == "" {
			continue // 両方空のエントリは無効
		}
		if m.ClassContains != "" && !strings.Contains(cn, strings.ToLower(m.ClassContains)) {
			continue
		}
		if m.TitleContains != "" && !strings.Contains(tt, strings.ToLower(m.TitleContains)) {
			continue
		}
		return true
	}
	return false
}

func getClassName(hwnd uintptr) string {
	var buf [256]uint16
	n, _, _ := procGetClassNameW.Call(
		hwnd,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
	)
	return windows.UTF16ToString(buf[:n])
}

// MoveExternalWindow は指定 HWND の左上 (Win32 GetWindowRect 互換の仮想デスクトップ座標) を
// (x, y) に移動する。サイズと Z 順は維持し、フォーカスは奪わない。SWP_ASYNCWINDOWPOS で
// 投げっぱなしにするので、対象プロセスが応答遅延でも本プロセスはブロックしない。
//
// 失敗 (UAC 越え・破棄済み HWND など) で SetWindowPos が 0 を返したら error。
// 呼び元 (WalkWithIE / FallWithIE / ThrowIE) はそのまま Action を終了する。
func MoveExternalWindow(hwnd uintptr, x, y int) error {
	if hwnd == 0 {
		return errors.New("platform: MoveExternalWindow called with hwnd=0")
	}
	ret, _, _ := procSetWindowPos.Call(
		hwnd,
		0, // hwndInsertAfter (SWP_NOZORDER で無視される)
		uintptr(int32(x)),
		uintptr(int32(y)),
		0, // cx (SWP_NOSIZE で無視される)
		0, // cy
		uintptr(swpNoSize|swpNoZOrder|swpNoActivate|swpAsyncWindowPos),
	)
	if ret == 0 {
		return errors.New("platform: SetWindowPos failed")
	}
	return nil
}

// IsExternalWindowGrabbable は指定 HWND がマスコットに「掴まれる」資格を満たすか判定する。
// 最大化中・最小化中・破棄済みは false。
//
// WalkWithIE / FallWithIE / ThrowIE の開始時に呼び、false なら Action を即終了して
// 別 Behavior を抽選しなおす。
func IsExternalWindowGrabbable(hwnd uintptr) bool {
	if hwnd == 0 {
		return false
	}
	if ok, _, _ := procIsWindow.Call(hwnd); ok == 0 {
		return false
	}
	if zoomed, _, _ := procIsZoomed.Call(hwnd); zoomed != 0 {
		return false
	}
	if iconic, _, _ := procIsIconic.Call(hwnd); iconic != 0 {
		return false
	}
	return true
}

// IsExternalWindowAlive は指定 HWND が依然として有効な (破棄されていない) ウィンドウかを返す。
// グラブ駆動中の毎 tick チェックで使う (消滅検知 → action 終了)。
func IsExternalWindowAlive(hwnd uintptr) bool {
	if hwnd == 0 {
		return false
	}
	ok, _, _ := procIsWindow.Call(hwnd)
	return ok != 0
}

// GetExternalWindowRect は指定 HWND の現在の矩形を返す。失敗時は (zero, false)。
// グラブ初期化時にウィンドウサイズを掴んでおくのに使う。
func GetExternalWindowRect(hwnd uintptr) (image.Rectangle, bool) {
	if hwnd == 0 {
		return image.Rectangle{}, false
	}
	var r rect
	ret, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	if ret == 0 {
		return image.Rectangle{}, false
	}
	return image.Rect(int(r.Left), int(r.Top), int(r.Right), int(r.Bottom)), true
}

func getWindowText(hwnd uintptr) string {
	n, _, _ := procGetWindowTextLengthW.Call(hwnd)
	if n == 0 {
		return ""
	}
	buf := make([]uint16, n+1)
	procGetWindowTextW.Call(
		hwnd,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
	)
	return windows.UTF16ToString(buf)
}
