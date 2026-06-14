package platform

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procCreatePopupMenu     = user32.NewProc("CreatePopupMenu")
	procAppendMenuW         = user32.NewProc("AppendMenuW")
	procTrackPopupMenu      = user32.NewProc("TrackPopupMenu")
	procDestroyMenu         = user32.NewProc("DestroyMenu")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
)

const (
	mfString    uintptr = 0x00000000
	mfPopup     uintptr = 0x00000010
	mfSeparator uintptr = 0x00000800
	mfGrayed    uintptr = 0x00000001

	tpmReturnCmd   uintptr = 0x0100
	tpmRightButton uintptr = 0x0002
)

// MenuItem は ShowPopupMenu に渡す 1 項目。
//   - Separator=true: 区切り線。他のフィールドは無視。
//   - len(Submenu) > 0: サブメニューを開く親項目 (ID は無視)。
//   - それ以外: クリック可能な leaf。ID は呼び出し側で 1 以上のユニーク値を割り振る。
//     ID=0 を返り値で受けたら「キャンセル」を意味するため leaf に 0 は使えない。
type MenuItem struct {
	ID        int
	Label     string
	Submenu   []MenuItem
	Separator bool
	Disabled  bool
}

// ShowPopupMenu は hwnd を所有ウィンドウとして screen 座標 (screenX, screenY) で
// コンテキストメニューを開き、ユーザーが選んだ leaf の MenuItem.ID を返す。
// メニュー外クリック等でキャンセルされた場合は 0。
//
// TrackPopupMenu はモーダル: メニューが閉じるまでブロックし、その間自前で
// メッセージ pump を回す (= 他ウィンドウへの入力も配送される。再入注意)。
// MSDN 既定で「メニューを出すウィンドウは foreground でなければ正しく dismiss されない」
// ため、呼び出し直前に SetForegroundWindow を入れる。
func ShowPopupMenu(hwnd uintptr, screenX, screenY int, items []MenuItem) int {
	hMenu, _, _ := procCreatePopupMenu.Call()
	if hMenu == 0 {
		return 0
	}
	// DestroyMenu はサブメニュー (MF_POPUP で attach 済み) も連鎖破棄するため、root のみ defer すれば足りる。
	defer procDestroyMenu.Call(hMenu)
	appendMenuItems(hMenu, items)

	procSetForegroundWindow.Call(hwnd)
	cmd, _, _ := procTrackPopupMenu.Call(
		hMenu,
		tpmReturnCmd|tpmRightButton,
		uintptr(screenX), uintptr(screenY),
		0,
		hwnd,
		0,
	)
	return int(cmd)
}

// appendMenuItems は items を hMenu に AppendMenuW で詰める。再帰でサブメニューも展開。
func appendMenuItems(hMenu uintptr, items []MenuItem) {
	for _, it := range items {
		if it.Separator {
			procAppendMenuW.Call(hMenu, mfSeparator, 0, 0)
			continue
		}
		labelPtr := windows.StringToUTF16Ptr(it.Label)
		if len(it.Submenu) > 0 {
			sub, _, _ := procCreatePopupMenu.Call()
			if sub == 0 {
				continue
			}
			appendMenuItems(sub, it.Submenu)
			flags := mfPopup
			if it.Disabled {
				flags |= mfGrayed
			}
			procAppendMenuW.Call(hMenu, flags, sub, uintptr(unsafe.Pointer(labelPtr)))
			continue
		}
		flags := mfString
		if it.Disabled {
			flags |= mfGrayed
		}
		procAppendMenuW.Call(hMenu, flags, uintptr(it.ID), uintptr(unsafe.Pointer(labelPtr)))
	}
}
