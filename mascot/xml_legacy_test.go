package mascot

import (
	"strings"
	"testing"
)

func TestLooksLegacyJapanese(t *testing.T) {
	cases := []struct {
		name string
		data string
		want bool
	}{
		{"legacy", `<?xml version="1.0"?><マスコット><動作リスト/></マスコット>`, true},
		{"kilkakon", `<?xml version="1.0"?><Mascot><ActionList/></Mascot>`, false},
		{"empty", ``, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := looksLegacyJapanese([]byte(c.data)); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestNormalizeLegacyJapaneseXML_PreservesNameValues(t *testing.T) {
	// Name 属性値 (日本語固有名) は絶対に翻訳されてはならない。
	in := `<?xml version="1.0"?>
<マスコット>
  <行動リスト>
    <行動 名前="マウスの周りに集まる" 頻度="50">
      <次の行動リスト 追加="false">
        <行動参照 名前="座ってマウスのほうを見る" 頻度="1"/>
      </次の行動リスト>
    </行動>
  </行動リスト>
</マスコット>`
	out, err := normalizeLegacyJapaneseXML([]byte(in))
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	s := string(out)

	// 要素・属性が英語化されていること
	for _, want := range []string{
		"<Mascot",
		"<BehaviorList",
		"<Behavior ",
		"<NextBehavior ",
		"<BehaviorReference ",
		`Name="マウスの周りに集まる"`,
		`Frequency="50"`,
		`Add="false"`,
		`Name="座ってマウスのほうを見る"`,
		`Frequency="1"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q\n--- output ---\n%s", want, s)
		}
	}
	// 日本語要素・属性は残っていないこと
	for _, bad := range []string{"<行動", "<次の行動リスト", "<行動参照", "名前=", "頻度=", "追加="} {
		if strings.Contains(s, bad) {
			t.Errorf("output still contains %q\n--- output ---\n%s", bad, s)
		}
	}
}

func TestNormalizeLegacyJapaneseXML_TranslatesEnumValues(t *testing.T) {
	in := `<?xml version="1.0"?>
<マスコット>
  <動作リスト>
    <動作 名前="走る" 種類="移動" 枠="地面">
      <アニメーション>
        <ポーズ 画像="/a.png" 基準座標="80,160" 移動速度="-4,0" 長さ="4"/>
      </アニメーション>
    </動作>
    <動作 名前="つままれる" 種類="組み込み" クラス="com.group_finity.mascot.action.Dragged"/>
  </動作リスト>
</マスコット>`
	out, err := normalizeLegacyJapaneseXML([]byte(in))
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	s := string(out)
	for _, want := range []string{
		`Type="Move"`,
		`BorderType="Floor"`,
		`Type="Embedded"`,
		`Class="com.group_finity.mascot.action.Dragged"`,
		`Image="/a.png"`,
		`ImageAnchor="80,160"`,
		`Velocity="-4,0"`,
		`Duration="4"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q\n--- output ---\n%s", want, s)
		}
	}
	// 列挙値の日本語が残っていないこと (動作の Name 属性に「走る」は残ってOK)
	for _, bad := range []string{`Type="移動"`, `BorderType="地面"`, `Type="組み込み"`} {
		if strings.Contains(s, bad) {
			t.Errorf("output still contains %q", bad)
		}
	}
	// Name 属性値は保持されること
	if !strings.Contains(s, `Name="走る"`) || !strings.Contains(s, `Name="つままれる"`) {
		t.Errorf("Name attribute values were translated incorrectly\n--- output ---\n%s", s)
	}
}
