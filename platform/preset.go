package platform

// Presets は出荷時に同梱する「投げて遊んでも作業に支障の出にくい」アプリ群。
// 一般ユーザが何も設定しなくても tray メニューから即 ON/OFF できる。
//
// ID は config.DisabledPresets で参照される識別子。バージョン間で安定させる必要があるため
// 一度公開した ID はリネームしない。表示名 (Label) は自由に変えてよい。
//
// 設計方針:
//   - ブラウザは exe マッチ (Chromium 系を class で巻き込むと VS Code / Slack / Discord
//     等の Electron アプリまで対象になってしまうため)
//   - UWP アプリは class=ApplicationFrameWindow + title (UWP は共通 class なので title 必須)
//   - クラシック Win32 アプリは class マッチ (メモ帳・ペイント)
//   - Terminal 系 / IDE / Office 等の「作業中の窓」は意図的に除外
func Presets() []Preset {
	return []Preset{
		// --- ブラウザ ---
		{ID: "chrome", Label: "Google Chrome", Matcher: WindowMatcher{ExeEquals: "chrome.exe"}},
		{ID: "edge", Label: "Microsoft Edge", Matcher: WindowMatcher{ExeEquals: "msedge.exe"}},
		{ID: "firefox", Label: "Firefox", Matcher: WindowMatcher{ExeEquals: "firefox.exe"}},
		{ID: "brave", Label: "Brave", Matcher: WindowMatcher{ExeEquals: "brave.exe"}},
		{ID: "vivaldi", Label: "Vivaldi", Matcher: WindowMatcher{ExeEquals: "vivaldi.exe"}},
		{ID: "opera", Label: "Opera", Matcher: WindowMatcher{ExeEquals: "opera.exe"}},

		// --- 純正アクセサリ (UWP) ---
		// title 部分一致なので「電卓」を含むウィンドウ全部にマッチする可能性は理論上あるが、
		// class=ApplicationFrameWindow との AND なので実害は無い。
		{ID: "calc", Label: "電卓", Matcher: WindowMatcher{ClassContains: "ApplicationFrameWindow", TitleContains: "電卓"}},
		{ID: "stickynotes", Label: "付箋", Matcher: WindowMatcher{ClassContains: "ApplicationFrameWindow", TitleContains: "付箋"}},
		{ID: "photos", Label: "フォト", Matcher: WindowMatcher{ClassContains: "ApplicationFrameWindow", TitleContains: "フォト"}},
		{ID: "mediaplayer", Label: "メディア プレーヤー", Matcher: WindowMatcher{ClassContains: "ApplicationFrameWindow", TitleContains: "メディア プレーヤー"}},

		// --- クラシック Win32 ---
		// メモ帳 (Windows 11 の新メモ帳も class=Notepad で同じ)
		{ID: "notepad", Label: "メモ帳", Matcher: WindowMatcher{ClassContains: "Notepad"}},
		// ペイント (クラシック版。新ペイント UWP は別途プリセットを足す余地あり)
		{ID: "paint", Label: "ペイント", Matcher: WindowMatcher{ClassContains: "MSPaintApp"}},
	}
}
