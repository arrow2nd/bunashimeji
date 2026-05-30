package platform

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Preset はアプリ同梱の「投げ対象アプリ」プリセット 1 件。
// ID はバージョン間で安定させる識別子 (config の DisabledPresets で参照)。
// Label は tray メニューに表示する人間向け名。
// Matcher は実際の WindowMatcher。
type Preset struct {
	ID      string
	Label   string
	Matcher WindowMatcher
}

// UserEntry は windows.json の "windows" 配列に並ぶユーザ追加分 1 件。
// label は tray 表示用、Exe/Class/Title は WindowMatcher にそのまま転写される。
// Enabled が nil (JSON で省略) なら true 扱い、明示 false で無効化。
type UserEntry struct {
	Label   string `json:"label"`
	Exe     string `json:"exe,omitempty"`
	Class   string `json:"class,omitempty"`
	Title   string `json:"title,omitempty"`
	Enabled *bool  `json:"enabled,omitempty"`
}

// IsEnabled は Enabled の三値 (nil/true/false) を bool に解決する。nil = true。
func (u UserEntry) IsEnabled() bool {
	return u.Enabled == nil || *u.Enabled
}

// Matcher は UserEntry を WindowMatcher に変換する。
func (u UserEntry) ToMatcher() WindowMatcher {
	return WindowMatcher{
		ClassContains: u.Class,
		TitleContains: u.Title,
		ExeEquals:     u.Exe,
	}
}

// WhitelistConfig は windows.json の root。
// Windows = ユーザ追加分、DisabledPresets = ユーザが無効化したプリセット ID 一覧。
type WhitelistConfig struct {
	Windows         []UserEntry `json:"windows"`
	DisabledPresets []string    `json:"disabledPresets,omitempty"`
}

// WhitelistEntry は tray が描画するための「プリセット + ユーザ追加分」の統一ビュー 1 件。
type WhitelistEntry struct {
	Label    string
	Matcher  WindowMatcher
	Enabled  bool
	IsPreset bool
	PresetID string // プリセットのみ。ユーザ追加分は ""
	UserIdx  int    // ユーザ追加分のみ。プリセットは -1
}

// 設定の現状を保持。tray の toggle 時にここを差し替え → 保存 → SetWhitelist を呼び直す。
var (
	configMu   sync.RWMutex
	currentCfg WhitelistConfig
	configPath string

	// saveMu はファイル保存を直列化する。configMu とは独立で、ファイル I/O 中に
	// configMu を長時間保持しないための分離。
	saveMu sync.Mutex
)

// LoadWhitelistConfig は path の JSON を読んで WhitelistConfig を返す。
// ファイル不在は (空 cfg, nil) を返す = デフォルト (全プリセット有効) で起動する想定。
// パースエラーは error を返すが、呼び元はログだけ吐いてデフォルト起動するのが安全。
func LoadWhitelistConfig(path string) (WhitelistConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return WhitelistConfig{}, nil
		}
		return WhitelistConfig{}, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg WhitelistConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return WhitelistConfig{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

// SaveWhitelistConfig は cfg を path に書き出す。
// 親ディレクトリが無ければ作成。temp ファイル + rename で原子的に置き換える。
func SaveWhitelistConfig(path string, cfg WhitelistConfig) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(path), ".windows.json.tmp.*")
	if err != nil {
		return fmt.Errorf("create tmp: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// InstallWhitelistConfig は起動時に 1 度だけ呼ぶ。
// path を「以後の保存先」として記録し、cfg を currentCfg に反映、SetWhitelist で実マッチに適用する。
func InstallWhitelistConfig(path string, cfg WhitelistConfig) {
	configMu.Lock()
	configPath = path
	currentCfg = cfg
	configMu.Unlock()
	applyCurrentConfigToWhitelist()
}

// EffectiveEntries はプリセット + ユーザ追加分を tray 表示用に並べて返す。
// プリセットが先、ユーザ追加分が後。プリセットの順序は Presets() の宣言順を保つ。
func EffectiveEntries() []WhitelistEntry {
	configMu.RLock()
	cfg := cloneConfig(currentCfg)
	configMu.RUnlock()

	disabled := map[string]bool{}
	for _, id := range cfg.DisabledPresets {
		disabled[id] = true
	}

	presets := Presets()
	out := make([]WhitelistEntry, 0, len(presets)+len(cfg.Windows))
	for _, p := range presets {
		out = append(out, WhitelistEntry{
			Label:    p.Label,
			Matcher:  p.Matcher,
			Enabled:  !disabled[p.ID],
			IsPreset: true,
			PresetID: p.ID,
			UserIdx:  -1,
		})
	}
	for i, u := range cfg.Windows {
		out = append(out, WhitelistEntry{
			Label:    u.Label,
			Matcher:  u.ToMatcher(),
			Enabled:  u.IsEnabled(),
			IsPreset: false,
			UserIdx:  i,
		})
	}
	return out
}

// TogglePreset はプリセット ID の有効/無効を切り替える。
// enabled=true なら DisabledPresets から除去、false なら追加。
// その後 config を保存しホワイトリストを再計算する。
func TogglePreset(presetID string, enabled bool) error {
	configMu.Lock()
	cfg := cloneConfig(currentCfg)
	cfg.DisabledPresets = removeString(cfg.DisabledPresets, presetID)
	if !enabled {
		cfg.DisabledPresets = append(cfg.DisabledPresets, presetID)
	}
	currentCfg = cfg
	configMu.Unlock()

	applyCurrentConfigToWhitelist()
	return saveCurrentConfig()
}

// ToggleUserEntry は config.Windows[idx] の Enabled を上書きする。
// idx 範囲外なら no-op。保存とホワイトリスト再計算まで連動。
func ToggleUserEntry(idx int, enabled bool) error {
	configMu.Lock()
	cfg := cloneConfig(currentCfg)
	if idx < 0 || idx >= len(cfg.Windows) {
		configMu.Unlock()
		return nil
	}
	b := enabled
	cfg.Windows[idx].Enabled = &b
	currentCfg = cfg
	configMu.Unlock()

	applyCurrentConfigToWhitelist()
	return saveCurrentConfig()
}

// saveCurrentConfig は最新の currentCfg をファイルに保存する。
// saveMu で直列化し、保存直前に currentCfg を re-read することで、並行 toggle 時に
// 古いスナップショットが最新を上書きする問題を防ぐ。
func saveCurrentConfig() error {
	saveMu.Lock()
	defer saveMu.Unlock()
	configMu.RLock()
	cfg := cloneConfig(currentCfg)
	path := configPath
	configMu.RUnlock()
	if path == "" {
		return nil
	}
	return SaveWhitelistConfig(path, cfg)
}

// applyCurrentConfigToWhitelist は currentCfg と Presets() から有効な WindowMatcher リストを
// 計算し SetWhitelist する。toggle / install から呼ばれる。
func applyCurrentConfigToWhitelist() {
	entries := EffectiveEntries()
	matchers := make([]WindowMatcher, 0, len(entries))
	for _, e := range entries {
		if !e.Enabled || e.Matcher.IsZero() {
			continue
		}
		matchers = append(matchers, e.Matcher)
	}
	SetWhitelist(matchers)
}

func cloneConfig(c WhitelistConfig) WhitelistConfig {
	out := WhitelistConfig{
		Windows:         make([]UserEntry, len(c.Windows)),
		DisabledPresets: append([]string{}, c.DisabledPresets...),
	}
	copy(out.Windows, c.Windows)
	// Enabled は *bool なので shallow copy で別ポインタを共有しないよう作り直す
	for i, u := range c.Windows {
		if u.Enabled != nil {
			b := *u.Enabled
			out.Windows[i].Enabled = &b
		}
	}
	return out
}

func removeString(s []string, target string) []string {
	out := s[:0]
	for _, v := range s {
		if !strings.EqualFold(v, target) {
			out = append(out, v)
		}
	}
	return out
}
