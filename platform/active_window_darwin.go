//go:build darwin

package platform

import (
	"errors"
	"image"
)

// ActiveWindow は darwin ではスタブ (常に Visible=false)。
type ActiveWindow struct {
	Visible   bool
	Rect      image.Rectangle
	ID        uintptr
	ClassName string
	Title     string
	Exe       string
}

// WindowMatcher は darwin ではスタブ (使われない)。
type WindowMatcher struct {
	ClassContains string
	TitleContains string
	ExeEquals     string
}

// IsZero は全フィールド空かを返す。darwin では使われないがビルドのため。
func (m WindowMatcher) IsZero() bool {
	return m.ClassContains == "" && m.TitleContains == "" && m.ExeEquals == ""
}

// GetActiveWindow は darwin では常に空 (Visible=false) を返す。
func GetActiveWindow() ActiveWindow { return ActiveWindow{} }

// SetWhitelist は darwin では no-op。
func SetWhitelist(_ []WindowMatcher) {}

// Whitelist は darwin では空スライスを返す。
func Whitelist() []WindowMatcher { return nil }

// MoveExternalWindow は darwin では未対応。
func MoveExternalWindow(_ uintptr, _, _ int) error {
	return errors.New("platform: MoveExternalWindow not supported on darwin")
}

// IsExternalWindowGrabbable は darwin では常に false。
func IsExternalWindowGrabbable(_ uintptr) bool { return false }

// IsExternalWindowAlive は darwin では常に false。
func IsExternalWindowAlive(_ uintptr) bool { return false }

// GetExternalWindowRect は darwin では常に (zero, false)。
func GetExternalWindowRect(_ uintptr) (image.Rectangle, bool) {
	return image.Rectangle{}, false
}
