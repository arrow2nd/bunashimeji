//go:build darwin

package platform

import (
	"context"
	"errors"
	"image"
	"time"
)

// ScreenInfo は 1 モニタ分の物理範囲と WorkArea。
type ScreenInfo struct {
	Monitor  image.Rectangle
	WorkArea image.Rectangle
}

// Screens は darwin ではスタブ。v1 ではサポート対象外。
func Screens() []ScreenInfo { return nil }

// CursorPosition は darwin ではスタブ。
func CursorPosition() image.Point { return image.Point{} }

// WindowHandlers は Win32Window のマウスイベントコールバック (darwin スタブ)。
type WindowHandlers struct {
	OnLeftDown  func(localX, localY int)
	OnLeftUp    func(localX, localY int)
	OnRightDown func(localX, localY int)
	OnMouseMove func(localX, localY int)
}

// WindowOpts は Win32Window のオプション (darwin スタブ)。
type WindowOpts struct {
	Title         string
	X, Y          int
	Width, Height int
	Layered       bool
	Handlers      WindowHandlers
}

// Win32Window は darwin ではスタブ (常にエラー)。
type Win32Window struct{}

// NewWin32Window は darwin では未対応。
func NewWin32Window(_ WindowOpts) (*Win32Window, error) {
	return nil, errors.New("platform: Win32Window not supported on darwin")
}

// Destroy は darwin では no-op。
func (w *Win32Window) Destroy() {}

// HWND は darwin では常に 0。
func (w *Win32Window) HWND() uintptr { return 0 }

// SetBitmap は darwin では未対応。
func (w *Win32Window) SetBitmap(_ *image.RGBA, _ image.Point) error {
	return errors.New("platform: SetBitmap not supported on darwin")
}

// SetClickMask は darwin では未対応。
func (w *Win32Window) SetClickMask(_ *image.RGBA) error {
	return errors.New("platform: SetClickMask not supported on darwin")
}

// Show は darwin では no-op。
func (w *Win32Window) Show() {}

// Hide は darwin では no-op。
func (w *Win32Window) Hide() {}

// PumpMessages は darwin では常に false (no-op)。
func PumpMessages() bool { return false }

// RunMessageLoop は darwin では未対応。
func RunMessageLoop(_ context.Context, _ time.Duration, _ func()) error {
	return errors.New("platform: RunMessageLoop not supported on darwin")
}
