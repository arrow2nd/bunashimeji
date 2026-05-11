package mascot

import "image"

// SpawnRequest は stepBreed が main.go 側に渡す新 Mascot 生成依頼。
//
// すべて parent 視点で計算済みの値で渡す:
//   - Anchor は仮想デスクトップ絶対座標 (parent.Anchor + (BornX, BornY) を解決済み、
//     LookRight=true なら BornX 反転済み)
//   - InitialBehavior は BornBehavior 属性の値そのまま (Behavior 名)
type SpawnRequest struct {
	ParentName      string
	Anchor          image.Point
	LookRight       bool
	InitialBehavior string
}

// Spawner は Mascot (action.go の stepBreed) と main.go の橋渡し。
//
// 実装上の制約:
//   - Spawn() は Tick 中に呼ばれる → 内部キューに積むだけで、実生成 (Win32 ウィンドウ作成等)
//     は行わない。Tick 中に既存 Mascot スライスを変更するとイテレータが崩壊する
//   - 実 Mascot/Window 生成は main.go の onTick callback 末尾でキューを drain して行う
//
// nil 可: テストや単独動作確認で Spawner を渡さない場合 stepBreed は no-op で受け流す。
type Spawner interface {
	Spawn(req SpawnRequest)
}
