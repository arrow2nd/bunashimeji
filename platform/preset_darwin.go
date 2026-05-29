//go:build darwin

package platform

// Presets は darwin では空 (activeIE 機能自体が未対応)。
func Presets() []Preset { return nil }
