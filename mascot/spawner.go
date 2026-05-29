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

// TransformRequest は stepTransform が main.go 側に渡す「自分を別キャラに置き換える」依頼。
//
// 流れ: アニメ完走 → Transform リクエスト発行 → 同 tick 末尾の drain で
//   1. NewName のテンプレート存在チェック
//   2. NewName のキャラを Self の Anchor / LookRight で新 spawn
//   3. Self の Character を Destroy して chars から除去
// の順で実行される (drain は spawn を先に行うことで、空 chars → cancel() の誤発火を防ぐ)。
type TransformRequest struct {
	Self            *Mascot // 変身元 (これを破棄する目印に使う)
	NewName         string  // TransformMascot 属性: 変身先キャラ名
	InitialBehavior string  // TransformBehavior 属性: 変身先キャラの起動 Behavior
}

// Spawner は Mascot (action.go の stepBreed / stepTransform) と main.go の橋渡し。
//
// 実装上の制約:
//   - Spawn() / Transform() は Tick 中に呼ばれる → 内部キューに積むだけで、実生成・破棄
//     (Win32 ウィンドウ作成/破棄等) は行わない。Tick 中に既存 Mascot スライスを変更すると
//     イテレータが崩壊する
//   - 実 Mascot/Window の生成・破棄は main.go の onTick callback 末尾でキューを drain
//     して行う
//
// nil 可: テストや単独動作確認で Spawner を渡さない場合 stepBreed / stepTransform は
// no-op で受け流す。
type Spawner interface {
	Spawn(req SpawnRequest)
	Transform(req TransformRequest)
}
