package mascot

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadAnzu はリポジトリ直下の conf/img を使って実キャラのパースを検証する。
// 資産が無い環境では SKIP する。
func TestLoadAnzu(t *testing.T) {
	root := filepath.Join("..")
	confDir := filepath.Join(root, "conf")
	imgDir := filepath.Join(root, "img")

	actions, err := LoadActions(confDir, "Anzu")
	if err != nil {
		t.Skipf("anzu actions not available: %v", err)
	}
	if len(actions) == 0 {
		t.Fatal("expected non-empty actions")
	}
	t.Logf("loaded %d actions", len(actions))

	behaviors, groups, consts, err := LoadBehaviors(confDir, "Anzu")
	if err != nil {
		t.Fatalf("load behaviors: %v", err)
	}
	if len(behaviors) == 0 && len(groups) == 0 {
		t.Fatal("expected behaviors")
	}
	t.Logf("loaded %d root behaviors, %d condition groups", len(behaviors), len(groups))

	// Anzu の behaviors.xml は <定数 Name="maxCount" 値="5" /> を持つ。
	// パースが成立して 5 が拾えていることを確認。
	if consts.MaxCount != 5 {
		t.Errorf("Anzu maxCount: got %d, want 5", consts.MaxCount)
	}

	// 必須 Behavior の存在確認
	required := []string{"ChaseMouse", "SitAndFaceMouse", "Fall", "Dragged", "Thrown"}
	all := append([]*Behavior{}, behaviors...)
	for _, g := range groups {
		all = append(all, g.Behaviors...)
	}
	for _, name := range required {
		found := false
		for _, b := range all {
			if b.Name == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("required behavior %q not found", name)
		}
	}

	// 画像ロード
	imgs, err := loadImages(imgDir, "Anzu")
	if err != nil {
		t.Fatalf("load images: %v", err)
	}
	t.Logf("loaded %d images", len(imgs))
	if len(imgs) == 0 {
		t.Fatal("expected images")
	}
}

// TestLoadLegacyJapanese は旧日本語版 XML (動作.xml / 行動.xml) を読み込めるかを検証する。
// testdata/legacy/ 配下にダミーフィクスチャを置いてあるので、外部資産なしで完結する。
func TestLoadLegacyJapanese(t *testing.T) {
	const charName = "legacy_jp"
	root := filepath.Join("testdata", "legacy")
	confDir := filepath.Join(root, "conf")
	imgDir := filepath.Join(root, "img")

	actions, err := LoadActions(confDir, charName)
	if err != nil {
		t.Fatalf("load actions: %v", err)
	}
	if len(actions) == 0 {
		t.Fatal("expected non-empty actions")
	}
	t.Logf("loaded %d actions", len(actions))

	// 動作.xml には <動作リスト> が 2 つあり、素材定義と複合動作が分かれている。
	// 両方からの Action がマージされていることを確認。
	if _, ok := actions["立つ"]; !ok {
		t.Error(`expected action "立つ" (from 1st 動作リスト) — ActionList merge may be broken`)
	}
	if _, ok := actions["立ってボーっとする"]; !ok {
		t.Error(`expected action "立ってボーっとする" (from 2nd 動作リスト) — ActionList merge may be broken`)
	}

	behaviors, groups, _, err := LoadBehaviors(confDir, charName)
	if err != nil {
		t.Fatalf("load behaviors: %v", err)
	}
	if len(behaviors) == 0 && len(groups) == 0 {
		t.Fatal("expected behaviors")
	}
	t.Logf("loaded %d root behaviors, %d condition groups", len(behaviors), len(groups))

	all := append([]*Behavior{}, behaviors...)
	for _, g := range groups {
		all = append(all, g.Behaviors...)
	}

	// 必須 Behavior が役割エイリアス経由で見つかるか確認。
	// 旧日本語版では日本語名 ("マウスの周りに集まる" 等) のみ存在する想定。
	requiredRoles := []string{"ChaseMouse", "SitAndFaceMouse", "Fall", "Dragged", "Thrown"}
	for _, role := range requiredRoles {
		found := false
		for _, name := range roleAliases[role] {
			for _, b := range all {
				if b.Name == name {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			t.Errorf("required role %q: no aliased behavior found", role)
		}
	}

	// 画像ロード
	imgs, err := loadImages(imgDir, charName)
	if err != nil {
		t.Fatalf("load images: %v", err)
	}
	t.Logf("loaded %d images", len(imgs))
	if _, ok := imgs["/dummy001.png"]; !ok {
		t.Error("expected /dummy001.png to be loaded")
	}
}

// TestLoadIEEmbeddedClasses は WalkWithIE / FallWithIE / ThrowIE の Class が
// XML パース時に正しく保持され、stepEmbedded の switch で dispatch 可能な形 (末尾だけ) に
// なっていることを確認する。
//
// 旧日本語版固有 Class は以前「未知 Embedded Class」として Animate にフォールバックしていたが、
// platform 層の外部ウィンドウ操作 API を追加した v1+ ではグラブ駆動の専用 step 関数を呼ぶ。
// Class 文字列が変質するとこの dispatch が失敗するため、回帰検出のための test。
func TestLoadIEEmbeddedClasses(t *testing.T) {
	dir := t.TempDir()
	confDir := filepath.Join(dir, "conf")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatalf("mkdir conf: %v", err)
	}
	xmlBody := `<?xml version="1.0" encoding="UTF-8" ?>
<Mascot>
  <ActionList>
    <Action Name="WalkAndDrag" Type="Embedded" Class="com.group_finity.mascot.action.WalkWithIE" BorderType="Floor" TargetX="${0}">
      <Animation>
        <Pose Image="/shime1.png" ImageAnchor="0,0" Velocity="-2,0" Duration="6" />
      </Animation>
    </Action>
    <Action Name="FallAndDrag" Type="Embedded" Class="FallWithIE">
      <Animation>
        <Pose Image="/shime1.png" ImageAnchor="0,0" Velocity="0,0" Duration="4" />
      </Animation>
    </Action>
    <Action Name="ThrowWindow" Type="Embedded" Class="com.group_finity.mascot.action.ThrowIE" InitialVX="${10}" InitialVY="${-15}">
      <Animation>
        <Pose Image="/shime1.png" ImageAnchor="0,0" Velocity="0,0" Duration="4" />
      </Animation>
    </Action>
  </ActionList>
</Mascot>
`
	if err := os.WriteFile(filepath.Join(confDir, "actions.xml"), []byte(xmlBody), 0o644); err != nil {
		t.Fatalf("write actions.xml: %v", err)
	}

	actions, err := LoadActions(confDir, "")
	if err != nil {
		t.Fatalf("LoadActions: %v", err)
	}

	cases := []struct {
		name      string
		wantClass string
	}{
		{"WalkAndDrag", "WalkWithIE"},
		{"FallAndDrag", "FallWithIE"},
		{"ThrowWindow", "ThrowIE"},
	}
	for _, c := range cases {
		a, ok := actions[c.name]
		if !ok {
			t.Errorf("action %q not found", c.name)
			continue
		}
		if a.Type != "Embedded" {
			t.Errorf("action %q: Type=%q, want Embedded", c.name, a.Type)
		}
		// classTail で FQCN 末尾だけ残る (例: "com.group_finity.mascot.action.WalkWithIE" → "WalkWithIE")。
		// stepEmbedded の switch がこの末尾文字列で dispatch するため、ここが崩れると
		// 「未知 Class」フォールバック (Animate) に逆戻りする。
		if a.Class != c.wantClass {
			t.Errorf("action %q: Class=%q, want %q", c.name, a.Class, c.wantClass)
		}
	}
}
