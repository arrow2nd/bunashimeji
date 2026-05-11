package mascot

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
)

// 旧日本語版 (オリジナル) の XML を本家英語版互換のタグに正規化するための変換表。
//
// 設計方針:
//   - 要素名 / 属性名 / 「Type と BorderType の値」だけを翻訳する
//   - Name 属性の値・テキストノード・条件式の中身は絶対に触らない
//     (例: <BehaviorReference Name="マウスの周りに集まる"> の Name 値はそのまま保持)
//
// Mascot.xsd を仕様根拠とする。

var legacyElementMap = map[string]string{
	"マスコット":   "Mascot",
	"動作リスト":   "ActionList",
	"行動リスト":   "BehaviorList",
	"動作":      "Action",
	"行動":      "Behavior",
	"動作参照":    "ActionReference",
	"行動参照":    "BehaviorReference",
	"アニメーション": "Animation",
	"ポーズ":     "Pose",
	"次の行動リスト": "NextBehavior",
	"条件":      "Condition",
	// "定数" は既存パーサが日本語タグ前提で対応済みなので翻訳しない
}

var legacyAttrMap = map[string]string{
	"名前":       "Name",
	"種類":       "Type",
	"枠":        "BorderType",
	"クラス":      "Class",
	"繰り返し":     "Loop",
	"頻度":       "Frequency",
	"追加":       "Add",
	"条件":       "Condition",
	"画像":       "Image",
	"基準座標":     "ImageAnchor",
	"移動速度":     "Velocity",
	"長さ":       "Duration",
	"目的地X":     "TargetX",
	"目的地Y":     "TargetY",
	"初速X":      "InitialVX",
	"初速Y":      "InitialVY",
	"右向き":      "LookRight",
	"重力":       "Gravity",
	// 旧日本語版「空気抵抗*」は本家の typo "Registance*" 系にマップする
	// (action.go の stepFalling 側で Registance* / Resistance* 両方を見ているので
	//  どちらでも受け取れるが、英語版の慣習に合わせて Registance* を採用)
	"空気抵抗X":    "RegistanceX",
	"空気抵抗Y":    "RegistanceY",
	"速度":       "Velocity",
	"生まれる場所X":  "BornX",
	"生まれる場所Y":  "BornY",
	"生まれた時の行動": "BornBehavior",
	"ずれ":       "Offset",
	"IEの端X":    "IEOffsetX",
	"IEの端Y":    "IEOffsetY",
}

var legacyTypeValueMap = map[string]string{
	"静止":   "Stay",
	"移動":   "Move",
	"固定":   "Animate",
	"複合":   "Sequence",
	"選択":   "Select",
	"組み込み": "Embedded",
}

var legacyBorderValueMap = map[string]string{
	"地面": "Floor",
	"壁":  "Wall",
	"天井": "Ceiling",
}

// paramAliasJP は Action パラメータの英語名 → 日本語名の対応表。
//
// プリプロセッサは XML の属性名を英語化するが、Animation Condition などの
// **式の本文** (テキストノードや Condition 属性の値) は触らないので、
// 旧日本語版コンテンツでは「目的地Y < mascot.anchor.y」のような日本語変数参照が
// そのまま残る。bindActionParamsToVM 側でこの対応表を引き、英語パラメータと
// 同じ値を日本語別名でも VM に公開することで、未定義参照エラーを防ぐ。
//
// 「目的地X」のようにパラメータ属性として typical なものだけ対象にする。
// 同じ英語名に複数の日本語が対応するケース (Velocity ↔ 移動速度/速度) では、
// パラメータ属性として現れる側 (=「速度」) を採用する。「移動速度」は Pose 属性で
// Params に入らないため衝突しない。
var paramAliasJP = map[string]string{
	"Duration":     "長さ",
	"TargetX":      "目的地X",
	"TargetY":      "目的地Y",
	"InitialVX":    "初速X",
	"InitialVY":    "初速Y",
	"LookRight":    "右向き",
	"Gravity":      "重力",
	"RegistanceX":  "空気抵抗X",
	"RegistanceY":  "空気抵抗Y",
	"Velocity":     "速度",
	"BornX":        "生まれる場所X",
	"BornY":        "生まれる場所Y",
	"BornBehavior": "生まれた時の行動",
	"Offset":       "ずれ",
	"IEOffsetX":    "IEの端X",
	"IEOffsetY":    "IEの端Y",
}

// looksLegacyJapanese はバイト列の最初の StartElement を見て、ルート要素が
// "マスコット" なら旧日本語版と判定する。stripNamespace 後に呼ぶ前提。
func looksLegacyJapanese(data []byte) bool {
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		if se, ok := tok.(xml.StartElement); ok {
			return se.Name.Local == "マスコット"
		}
	}
}

// normalizeLegacyJapaneseXML は旧日本語版 XML を要素名・属性名・列挙値レベルで
// 本家英語版互換に変換する。Token 単位で走査するので Name 属性値などペイロードは
// 一切触らない。
func normalizeLegacyJapaneseXML(data []byte) ([]byte, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var buf bytes.Buffer
	enc := xml.NewEncoder(&buf)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("normalize legacy xml: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			// 名前空間 (デコーダが付与した Name.Space) は Mascot 名前空間を捨てる:
			// stripNamespace と整合させるため、出力時に xmlns 属性が再付与されないよう
			// Space を空にしておく。
			t.Name.Space = ""
			if en, ok := legacyElementMap[t.Name.Local]; ok {
				t.Name.Local = en
			}
			for i := range t.Attr {
				t.Attr[i].Name.Space = ""
				if en, ok := legacyAttrMap[t.Attr[i].Name.Local]; ok {
					t.Attr[i].Name.Local = en
				}
				switch t.Attr[i].Name.Local {
				case "Type":
					if v, ok := legacyTypeValueMap[t.Attr[i].Value]; ok {
						t.Attr[i].Value = v
					}
				case "BorderType":
					if v, ok := legacyBorderValueMap[t.Attr[i].Value]; ok {
						t.Attr[i].Value = v
					}
				}
			}
			if err := enc.EncodeToken(t); err != nil {
				return nil, err
			}
		case xml.EndElement:
			t.Name.Space = ""
			if en, ok := legacyElementMap[t.Name.Local]; ok {
				t.Name.Local = en
			}
			if err := enc.EncodeToken(t); err != nil {
				return nil, err
			}
		default:
			if err := enc.EncodeToken(tok); err != nil {
				return nil, err
			}
		}
	}
	if err := enc.Flush(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
