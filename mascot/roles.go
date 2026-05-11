package mascot

// 必須 Behavior の役割名 → XML 上で実際に書かれている可能性のある名前リスト。
//
// 本家英語版は英語名を使うが、オリジナル日本語版は同じ役割を日本語名で書く。
// エンジン側は「役割」で参照し、registry が候補名を
// 順に探して最初にヒットしたものを採用する。
//
// 役割名そのもの (英語側) は historical reason で本家英語版の名前と一致させている。
var roleAliases = map[string][]string{
	"ChaseMouse":      {"ChaseMouse", "マウスの周りに集まる"},
	"SitAndFaceMouse": {"SitAndFaceMouse", "座ってマウスのほうを見る"},
	"Fall":            {"Fall", "落下する"},
	"Dragged":         {"Dragged", "ドラッグされる"},
	"Thrown":          {"Thrown", "投げられる"},
}

// findBehaviorByRole は役割名から実体 Behavior を探す。
// roleAliases に登録されていない名前は素の findBehaviorByName と同じ動作。
func (m *Mascot) findBehaviorByRole(role string) (*Behavior, bool) {
	for _, name := range roleAliases[role] {
		if b, ok := m.findBehaviorByName(name); ok {
			return b, true
		}
	}
	// alias 未定義 = 役割ではなく普通の名前。素の検索にフォールバック
	if _, listed := roleAliases[role]; !listed {
		return m.findBehaviorByName(role)
	}
	return nil, false
}

// behaviorMatchesRole は b が role の候補名のいずれかに一致するか判定する。
// 「現 Behavior が Fall か?」のような状態比較に使う。
func behaviorMatchesRole(b *Behavior, role string) bool {
	if b == nil {
		return false
	}
	aliases, ok := roleAliases[role]
	if !ok {
		return b.Name == role
	}
	for _, name := range aliases {
		if b.Name == name {
			return true
		}
	}
	return false
}
