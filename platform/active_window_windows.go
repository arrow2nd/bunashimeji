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
	Exe       string // プロセス実行ファイル basename (例: "chrome.exe"), 取得失敗時は空
}

// WindowMatcher はホワイトリスト 1 件分のマッチ条件。
// ClassContains / TitleContains は case-insensitive の部分一致。
// ExeEquals はプロセス実行ファイル basename の case-insensitive 完全一致 (例: "chrome.exe")。
// 複数指定なら AND マッチ、空のフィールドは評価しない。
// 全フィールドが空のエントリは無効 (誤って全マッチさせないため)。
type WindowMatcher struct {
	ClassContains string
	TitleContains string
	ExeEquals     string
}

// IsZero はマッチ条件が一つも指定されていない (=無効エントリ) かを返す。
func (m WindowMatcher) IsZero() bool {
	return m.ClassContains == "" && m.TitleContains == "" && m.ExeEquals == ""
}

// 自プロセスのマスコットウィンドウのクラス名 (window_windows.go の RegisterClassExW で
// 登録するもの)。フォアグラウンド検出時に除外する。
const ownWindowClassName = "BunashimejiWindow"

var (
	whitelistMu sync.RWMutex
	// 起動初期は全プリセット有効。main から ApplyWhitelistConfig() で
	// ユーザ設定を反映するまでの暫定値。
	whitelist = initialWhitelist()
)

func initialWhitelist() []WindowMatcher {
	presets := Presets()
	out := make([]WindowMatcher, 0, len(presets))
	for _, p := range presets {
		out = append(out, p.Matcher)
	}
	return out
}

// SetWhitelist はホワイトリストを差し替える。
// config ファイルからロードする際のエントリポイント。
// nil または空スライスを渡すとホワイトリストが無効化され、ActiveWindow.Visible は常に false。
func SetWhitelist(matchers []WindowMatcher) {
	whitelistMu.Lock()
	defer whitelistMu.Unlock()
	whitelist = append([]WindowMatcher{}, matchers...)
	// キャッシュを無効化して次回呼び出しで再評価
	awCacheTime = time.Time{}
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
	procGetForegroundWindow       = user32.NewProc("GetForegroundWindow")
	procGetWindowRect             = user32.NewProc("GetWindowRect")
	procGetClassNameW             = user32.NewProc("GetClassNameW")
	procGetWindowTextW            = user32.NewProc("GetWindowTextW")
	procGetWindowTextLengthW      = user32.NewProc("GetWindowTextLengthW")
	procIsWindowVisible           = user32.NewProc("IsWindowVisible")
	procIsIconic                  = user32.NewProc("IsIconic")
	procIsZoomed                  = user32.NewProc("IsZoomed")
	procIsWindow                  = user32.NewProc("IsWindow")
	procGetWindowThreadProcessId  = user32.NewProc("GetWindowThreadProcessId")
	procOpenProcess               = kernel32.NewProc("OpenProcess")
	procCloseHandle               = kernel32.NewProc("CloseHandle")
	procQueryFullProcessImageName = kernel32.NewProc("QueryFullProcessImageNameW")
)

// OpenProcess に渡すアクセス権。PROCESS_QUERY_LIMITED_INFORMATION (0x1000) は
// Vista 以降で他プロセスの exe path 取得に必要十分かつ最小権限。
// PROCESS_QUERY_INFORMATION (0x0400) より制限が緩く UAC 越えにも比較的強い。
const processQueryLimitedInformation uintptr = 0x1000

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
	exe := getProcessExeName(hwnd) // 失敗時は "" (UAC 越えや system プロセスで起こりうる)

	if !matchesWhitelist(className, title, exe) {
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
		Exe:       exe,
	}
}

func matchesWhitelist(className, title, exe string) bool {
	whitelistMu.RLock()
	defer whitelistMu.RUnlock()
	cn := strings.ToLower(className)
	tt := strings.ToLower(title)
	ex := strings.ToLower(exe)
	for _, m := range whitelist {
		if m.IsZero() {
			continue // 全フィールド空のエントリは無効
		}
		if m.ClassContains != "" && !strings.Contains(cn, strings.ToLower(m.ClassContains)) {
			continue
		}
		if m.TitleContains != "" && !strings.Contains(tt, strings.ToLower(m.TitleContains)) {
			continue
		}
		if m.ExeEquals != "" && ex != strings.ToLower(m.ExeEquals) {
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

// getProcessExeName は hwnd を所有するプロセスの実行ファイル basename を返す
// (例: `C:\Program Files\Google\Chrome\Application\chrome.exe` → `chrome.exe`)。
// 取得失敗 (UAC 越え・終了済み等) は空文字列。
//
// 3 段階の syscall:
//   1. GetWindowThreadProcessId(hwnd, &pid)
//   2. OpenProcess(QUERY_LIMITED_INFORMATION, false, pid) → handle
//   3. QueryFullProcessImageNameW(handle, 0, buf, &len) → full path
//
// QueryFullProcessImageNameW は GetModuleFileNameEx 系より UAC・WOW64 越境に
// 強く、QUERY_LIMITED_INFORMATION (Vista+) で済む。
func getProcessExeName(hwnd uintptr) string {
	var pid uint32
	procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid == 0 {
		return ""
	}
	h, _, _ := procOpenProcess.Call(processQueryLimitedInformation, 0, uintptr(pid))
	if h == 0 {
		return ""
	}
	defer procCloseHandle.Call(h)

	var buf [windows.MAX_PATH]uint16
	size := uint32(len(buf))
	ret, _, _ := procQueryFullProcessImageName.Call(
		h,
		0, // dwFlags=0 → Win32 path
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
	)
	if ret == 0 {
		return ""
	}
	full := windows.UTF16ToString(buf[:size])
	// basename だけ返す (パスは比較に使わない)
	if i := strings.LastIndexAny(full, `\/`); i >= 0 {
		return full[i+1:]
	}
	return full
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
