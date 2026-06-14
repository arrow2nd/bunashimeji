//go:build darwin

package platform

// MenuItem は ShowPopupMenu の項目仕様。Windows と API を揃えるためのスタブ定義。
type MenuItem struct {
	ID        int
	Label     string
	Submenu   []MenuItem
	Separator bool
	Disabled  bool
}

// ShowPopupMenu は macOS では未実装。常に 0 (キャンセル相当) を返す。
func ShowPopupMenu(hwnd uintptr, screenX, screenY int, items []MenuItem) int {
	return 0
}
