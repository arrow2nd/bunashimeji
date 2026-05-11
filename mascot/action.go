package mascot

import (
	"fmt"
	"image"
	"log"

	"github.com/arrow2nd/bunashimeji/platform"
)

// newActionState は Action 開始時の ActionState を作る。
// ${} 評価結果のキャッシュ用 map を初期化する。
func newActionState(a *Action, m *Mascot) *ActionState {
	s := &ActionState{
		Action:       a,
		CachedParams: map[string]any{},
		StartTick:    m.tick,
		StartAnchor:  m.Anchor,
	}
	// ${} を含むパラメータは Action 開始時に評価してキャッシュ
	for name, ev := range a.Params {
		if ev == nil || !ev.HasOnce() {
			continue
		}
		v, err := ev.EvalValue(m.vm, s.CachedParams)
		if err != nil {
			log.Printf("warning: action %q param %q eval: %v", a.Name, name, err)
			continue
		}
		s.CachedParams["param:"+name] = v
	}
	if m.OnActionEnter != nil {
		m.OnActionEnter(a.Name, a.Type)
	}
	return s
}

// StepAction は ActionState を 1 フレーム進める。
// done=true で完了 (Behavior 側に次へ進めるよう通知)。
func StepAction(s *ActionState, m *Mascot, env *Environment) (done bool) {
	if s == nil || s.Action == nil {
		return true
	}
	switch s.Action.Type {
	case "Stay":
		return stepStay(s, m)
	case "Animate":
		return stepAnimate(s, m, env)
	case "Move":
		return stepMove(s, m, env)
	case "Sequence":
		return stepSequence(s, m, env)
	case "Select":
		return stepSelect(s, m, env)
	case "Embedded":
		return stepEmbedded(s, m, env)
	default:
		log.Printf("warning: unknown action type %q (%s)", s.Action.Type, s.Action.Name)
		return true
	}
}

func currentPoseOf(s *ActionState, m *Mascot, env *Environment) (Pose, bool) {
	if s == nil {
		return Pose{}, false
	}
	if s.ChildState != nil {
		return currentPoseOf(s.ChildState, m, env)
	}
	anim := pickAnimation(s, m)
	if anim == nil || len(anim.Poses) == 0 {
		return Pose{}, false
	}
	if s.PoseIndex >= len(anim.Poses) {
		return anim.Poses[len(anim.Poses)-1], true
	}
	return anim.Poses[s.PoseIndex], true
}

// bindActionParamsToVM は Action のパラメータ全てを VM のグローバル変数として公開する。
// 本家は Animation の Condition 式で `TargetY < mascot.anchor.y` のように
// パラメータを直接参照することを許す。pickAnimation の直前に呼ぶ。
//
// $-only param は CachedParams から、#-含む param は毎回評価する。
// param 間の依存解決は行わない (ABI 上一段階のみ)。
func bindActionParamsToVM(s *ActionState, m *Mascot) {
	if s == nil || s.Action == nil {
		return
	}
	for name, ev := range s.Action.Params {
		if ev == nil {
			continue
		}
		v, err := ev.EvalValue(m.vm, s.CachedParams)
		if err != nil {
			// 解決できないパラメータは静かにスキップ (元から undefined が許容される設計)
			continue
		}
		m.vm.Set(name, v)
		// 旧日本語版コンテンツの Animation Condition は属性値が英語化された後も
		// 本文中で「目的地Y」等の日本語名のまま変数参照する。同じ値を日本語別名でも公開。
		if jp, ok := paramAliasJP[name]; ok {
			m.vm.Set(jp, v)
		}
	}
}

// pickAnimation は ActionState から param を VM へバインドした上で、
// 条件にマッチする最初の Animation を返す。
func pickAnimation(s *ActionState, m *Mascot) *Animation {
	bindActionParamsToVM(s, m)
	return pickAnimationByAction(s.Action, m, s.CachedParams)
}

// pickAnimationByAction は param バインドを行わず、与えられた cache だけで Animation 条件を評価する。
// Select の子 Action 候補チェックなど、まだ ActionState を作っていない場面で使用する。
func pickAnimationByAction(a *Action, m *Mascot, cached map[string]any) *Animation {
	for i := range a.Animations {
		anim := &a.Animations[i]
		ok, err := anim.Condition.EvalBool(m.vm, cached)
		if err != nil {
			log.Printf("warning: action %q animation condition: %v", a.Name, err)
			continue
		}
		if ok {
			return anim
		}
	}
	return nil
}

// advanceAnimation は 1 周分だけ Pose を進める (Animate / Sequence 用)。
// loop=true ならインデックスを循環させ、Animation 完了とは見なさない (Move 用)。
//
// 本家の Pose Velocity は「スプライトの前進方向」を表し、
// 元絵は左向きで描かれているため LookRight=true (右向き表示) のとき X 方向を反転する。
func advanceAnimation(s *ActionState, m *Mascot, _ *Environment, loop bool) bool {
	anim := pickAnimation(s, m)
	if anim == nil || len(anim.Poses) == 0 {
		return true
	}
	if !loop && s.PoseIndex >= len(anim.Poses) {
		return true
	}
	idx := s.PoseIndex
	if loop {
		idx %= len(anim.Poses)
	}
	pose := anim.Poses[idx]
	dx := pose.Velocity.X
	if m.LookRight {
		dx = -dx
	}
	m.Anchor.X += dx
	m.Anchor.Y += pose.Velocity.Y
	m.Velocity = image.Point{X: dx, Y: pose.Velocity.Y}
	s.PoseTick++
	if s.PoseTick >= pose.Duration {
		s.PoseTick = 0
		s.PoseIndex++
		if loop && s.PoseIndex >= len(anim.Poses) {
			s.PoseIndex = 0
		}
	}
	return !loop && s.PoseIndex >= len(anim.Poses)
}

// ----------- Type 別実装 -----------

func stepStay(s *ActionState, m *Mascot) bool {
	// Animation があれば再生、なければ Duration まで何もしない
	if len(s.Action.Animations) > 0 {
		anim := pickAnimation(s, m)
		if anim != nil && len(anim.Poses) > 0 {
			pose := anim.Poses[s.PoseIndex%len(anim.Poses)]
			m.Velocity = image.Point{}
			s.PoseTick++
			if s.PoseTick >= pose.Duration {
				s.PoseTick = 0
				s.PoseIndex++
			}
			if s.PoseIndex >= len(anim.Poses) {
				return true
			}
			return false
		}
	}
	// Duration パラメータが指定されていれば従う、なければ 1 tick で終了
	if ev, ok := s.Action.Params["Duration"]; ok {
		dur, _ := ev.EvalInt(m.vm, s.CachedParams)
		if m.tick-s.StartTick >= dur {
			return true
		}
		return false
	}
	return true
}

func stepAnimate(s *ActionState, m *Mascot, env *Environment) bool {
	return advanceAnimation(s, m, env, false)
}

func stepMove(s *ActionState, m *Mascot, env *Environment) bool {
	// Action 開始時に進行方向と LookRight を確定する。
	// その後は「進行方向に Target を通過した瞬間」に完了とする。
	// 距離絶対値 (abs <= 2) で判定すると TargetX がランダムで anchor.X 付近に
	// 落ちた場合に 1 tick で終了してしまうので、方向通過方式にする。
	if init, _ := s.CachedParams["_moveinit"].(bool); !init {
		s.CachedParams["_moveinit"] = true
		var tx, ty int
		hasTX, hasTY := false, false
		if ev, ok := s.Action.Params["TargetX"]; ok && ev != nil {
			tx, _ = ev.EvalInt(m.vm, s.CachedParams)
			hasTX = true
			s.CachedParams["_targetX"] = tx
			switch {
			case tx > m.Anchor.X:
				m.LookRight = true
				s.CachedParams["_dirX"] = 1
			case tx < m.Anchor.X:
				m.LookRight = false
				s.CachedParams["_dirX"] = -1
			default:
				s.CachedParams["_dirX"] = 0
			}
		}
		if ev, ok := s.Action.Params["TargetY"]; ok && ev != nil {
			ty, _ = ev.EvalInt(m.vm, s.CachedParams)
			hasTY = true
			s.CachedParams["_targetY"] = ty
			switch {
			case ty > m.Anchor.Y:
				s.CachedParams["_dirY"] = 1
			case ty < m.Anchor.Y:
				s.CachedParams["_dirY"] = -1
			default:
				s.CachedParams["_dirY"] = 0
			}
		}
		if m.OnMoveStart != nil && (hasTX || hasTY) {
			t := m.Anchor
			if hasTX {
				t.X = tx
			}
			if hasTY {
				t.Y = ty
			}
			m.OnMoveStart(s.Action.Name, t, m.Anchor)
		}
	}
	// Animation はループ再生 (TargetX/Y 到達か境界衝突で終了)
	advanceAnimation(s, m, env, true)

	// X 方向通過判定
	if dx, hasX := s.CachedParams["_dirX"].(int); hasX {
		tx := toInt(s.CachedParams["_targetX"])
		switch {
		case dx == 0:
			return true
		case dx > 0 && m.Anchor.X >= tx:
			return true
		case dx < 0 && m.Anchor.X <= tx:
			return true
		}
	}
	// Y 方向通過判定
	if dy, hasY := s.CachedParams["_dirY"].(int); hasY {
		ty := toInt(s.CachedParams["_targetY"])
		switch {
		case dy == 0:
			return true
		case dy > 0 && m.Anchor.Y >= ty:
			return true
		case dy < 0 && m.Anchor.Y <= ty:
			return true
		}
	}
	// BorderType は「該当境界に接している間のみ有効」 = 接していない瞬間に終了する。
	// 例: Walk は BorderType="Floor" → 床から落ちたら終了 (Fall に遷移)
	switch s.Action.BorderType {
	case "Floor":
		if !env.IsOnFloor(m.Anchor) {
			return true
		}
	case "Wall":
		if !env.IsOnWall(m.Anchor) {
			return true
		}
	case "Ceiling":
		if !env.IsOnCeiling(m.Anchor) {
			return true
		}
	}
	return false
}

func stepSequence(s *ActionState, m *Mascot, env *Environment) bool {
	if len(s.Action.Children) == 0 {
		return true
	}
	for {
		if s.SequenceStep >= len(s.Action.Children) {
			if s.Action.Loop {
				s.SequenceStep = 0
				s.ChildState = nil
				continue
			}
			return true
		}
		if s.ChildState == nil {
			s.ChildState = newActionState(s.Action.Children[s.SequenceStep], m)
		}
		done := StepAction(s.ChildState, m, env)
		if !done {
			return false
		}
		s.ChildState = nil
		s.SequenceStep++
	}
}

func stepSelect(s *ActionState, m *Mascot, env *Environment) bool {
	if s.ChildState != nil {
		return StepAction(s.ChildState, m, env)
	}
	for _, ch := range s.Action.Children {
		// Children は ActionReference 解決済み。条件は Animation Condition で見る運用が多いが、
		// 本家の Select は子の Condition 属性で分岐する場合もある。ここでは
		// 子の Animation 条件 + 子の Params["Condition"] の両方を見る。
		if ev, ok := ch.Params["Condition"]; ok && ev != nil {
			if ok2, err := ev.EvalBool(m.vm, nil); err == nil && !ok2 {
				continue
			}
		}
		// 子の Animation 条件には ActionState がまだ無いので param バインドを行わない。
		// 子が param 名を直接参照する条件式を持つ場合 (稀) はこのチェックをすり抜けて
		// 通常の StepAction → pickAnimation(s, m) で正しく評価される。
		if pickAnimationByAction(ch, m, nil) == nil && len(ch.Animations) > 0 {
			continue
		}
		s.ChildState = newActionState(ch, m)
		return StepAction(s.ChildState, m, env)
	}
	return true
}

func stepEmbedded(s *ActionState, m *Mascot, env *Environment) bool {
	// 本家の Java FQCN 末尾は Fall/Jump/Look/Offset/... など。
	// ユーザ XML の Class はバリエーションがあるため alias を許容する。
	switch s.Action.Class {
	case "Look", "Turn":
		return stepLook(s, m)
	case "Offset", "Move":
		return stepOffset(s, m)
	case "Fall", "Falling":
		return stepFalling(s, m, env)
	case "Jump", "Jumping":
		return stepJumping(s, m, env)
	case "Dragged":
		return stepDragged(s, m)
	case "Broadcast":
		return stepBroadcast(s, m, env)
	case "ScanMove":
		return stepScanMove(s, m, env)
	case "Breed":
		return stepBreed(s, m, env)
	case "WalkWithIE":
		return stepWalkWithIE(s, m, env)
	case "FallWithIE":
		return stepFallWithIE(s, m, env)
	case "ThrowIE":
		return stepThrowIE(s, m, env)
	case "Regist", "Interact", "BroadcastPosition":
		// 登録/連携系 (v1 では no-op)
		return true
	case "Sound", "Transform":
		// Sound (音声) / Transform (キャラ姿変化) は v1 スコープ外。no-op で受け流す。
		return true
	default:
		// 未知 Embedded Class は Animate と同じく Pose を完走させて受け流す。
		// 即終了させると 1 tick で抜けて連鎖アクションのアニメーションが全く見えなくなる。
		// ログは Action 開始時に 1 回だけ。
		if _, warned := s.CachedParams["_unknownClassWarned"]; !warned {
			log.Printf("warning: unknown embedded class %q (%s) — falling back to Animate", s.Action.Class, s.Action.Name)
			s.CachedParams["_unknownClassWarned"] = true
		}
		return advanceAnimation(s, m, env, false)
	}
}

// ----------- 外部ウィンドウグラブ系 (WalkWithIE / FallWithIE / ThrowIE) -----------

// ensureGrabbed は初 tick で activeIE の HWND を確保し、Mascot にグラブ状態をセットする。
// 失敗時 (activeIE 非表示・最大化・破棄済み・UAC 越え) は false を返し、呼び元は Action を
// 即終了して別 Behavior を抽選しなおす。2 回目以降の呼び出しは結果をキャッシュから返す。
func ensureGrabbed(s *ActionState, m *Mascot, env *Environment) bool {
	if init, _ := s.CachedParams["_grabInit"].(bool); init {
		ok, _ := s.CachedParams["_grabOK"].(bool)
		return ok
	}
	s.CachedParams["_grabInit"] = true

	aw := env.ActiveWindow
	if !aw.Visible || aw.ID == 0 || !platform.IsExternalWindowGrabbable(aw.ID) {
		s.CachedParams["_grabOK"] = false
		return false
	}

	// オフセット決定:
	//   本家仕様: IeOffsetX / IeOffsetY は「**LookRight=true 向き (右向き)** の場合の
	//   ウィンドウ**左下隅**から mascot.Anchor までの差」を指定する。
	//   `IeOffsetX="0" IeOffsetY="-64"` の意味:
	//     LookRight=true  → window.left = mascot.x          (mascot は window の左下隅)
	//                       window.bottom = mascot.y - 64
	//     LookRight=false → window.right = mascot.x         (mascot は window の右下隅)
	//                       window.bottom = mascot.y - 64
	//   X 側を LookRight に応じて反転するルールは `BornX` と同じ (`stepBreed` 参照)。
	//   Y 側は LookRight で反転しない (上下は鏡像にならない)。
	//
	//   内部の grabbedOffset は「ウィンドウ左上 (Win32 SetWindowPos の入力) と
	//   mascot.Anchor の差」で保持するので、入力時に次のように変換する:
	//     LookRight=true:  offset.X = IeOffsetX
	//     LookRight=false: offset.X = -IeOffsetX - windowWidth
	//     offset.Y       = IeOffsetY - windowHeight   (常に)
	//
	//   未指定なら現在の相対位置 (window.Min - mascot.Anchor) をそのまま維持 (= 動的)。
	//   この場合スナップは起きず、Action 開始時の見た目をそのまま引き継ぐ。
	winW := aw.Rect.Dx()
	winH := aw.Rect.Dy()
	offset := image.Point{
		X: aw.Rect.Min.X - m.Anchor.X,
		Y: aw.Rect.Min.Y - m.Anchor.Y,
	}
	hasSnap := false
	if ev, ok := s.Action.Params["IeOffsetX"]; ok && ev != nil {
		if v, err := ev.EvalInt(m.vm, s.CachedParams); err == nil {
			if m.LookRight {
				offset.X = v
			} else {
				offset.X = -v - winW
			}
			hasSnap = true
		}
	}
	if ev, ok := s.Action.Params["IeOffsetY"]; ok && ev != nil {
		if v, err := ev.EvalInt(m.vm, s.CachedParams); err == nil {
			// IeOffsetY は左下基準 → 左上 (Win32 SetWindowPos) に変換
			offset.Y = v - winH
			hasSnap = true
		}
	}

	m.setGrab(aw.ID, offset)
	s.CachedParams["_grabOK"] = true
	s.CachedParams["_winW"] = winW
	s.CachedParams["_winH"] = winH

	// スナップ指定があれば Action 開始時の 1 tick でウィンドウを瞬時に再配置する。
	// driveGrabbedWindow は MoveExternalWindow を SWP_ASYNCWINDOWPOS で投げるので、
	// 呼び出し自体は非ブロッキング。マスコットの 1 動作目の描画と同じタイミングで
	// ウィンドウが「抱える位置」へワープする視覚効果になる。
	if hasSnap {
		if !driveGrabbedWindow(m) {
			s.CachedParams["_grabOK"] = false
			return false
		}
	}
	return true
}

// driveGrabbedWindow は anchor + grabbedOffset の位置に外部ウィンドウを移動する。
// ウィンドウが消滅・破棄されていたら false を返す (呼び元は Action 終了)。
func driveGrabbedWindow(m *Mascot) bool {
	if m.grabbedHWND == 0 {
		return false
	}
	if !platform.IsExternalWindowAlive(m.grabbedHWND) {
		m.clearGrab()
		return false
	}
	x := m.Anchor.X + m.grabbedOffset.X
	y := m.Anchor.Y + m.grabbedOffset.Y
	if err := platform.MoveExternalWindow(m.grabbedHWND, x, y); err != nil {
		m.clearGrab()
		return false
	}
	return true
}

// stepWalkWithIE は外部ウィンドウを掴んだまま歩く。
// 歩行ロジックは stepMove に委譲し、毎 tick の末で駆動 (driveGrabbedWindow) する。
// TargetX 到達 / BorderType 外し / ウィンドウ消失で完了。
func stepWalkWithIE(s *ActionState, m *Mascot, env *Environment) bool {
	if _, tried := s.CachedParams["_grabInit"]; !tried {
		if !ensureGrabbed(s, m, env) {
			return true
		}
	}
	done := stepMove(s, m, env)
	if !driveGrabbedWindow(m) {
		done = true
	}
	if done {
		m.clearGrab()
	}
	return done
}

// stepFallWithIE は外部ウィンドウを掴んだまま落下する。
// 落下ロジックは stepFalling に委譲。床着地・速度減衰・ウィンドウ消失で完了。
func stepFallWithIE(s *ActionState, m *Mascot, env *Environment) bool {
	if _, tried := s.CachedParams["_grabInit"]; !tried {
		if !ensureGrabbed(s, m, env) {
			return true
		}
	}
	done := stepFalling(s, m, env)
	if !driveGrabbedWindow(m) {
		done = true
	}
	if done {
		m.clearGrab()
	}
	return done
}

// stepThrowIE は外部ウィンドウを投擲する。
//
// 本家仕様: マスコットは「投げた」あとその場に留まり、ウィンドウだけが
// 物理運動で飛んでいく。よって stepFalling と異なり m.Anchor は触らず、
// **ウィンドウ位置 (wx, wy) を独立した状態として CachedParams に保持** する。
// driveGrabbedWindow (anchor + offset を使う) ではなく直接 MoveExternalWindow を呼ぶ。
//
// 境界判定は全モニタの WorkArea 連合 (WorkAreaUnion) を使い、ウィンドウが
// マルチモニタにまたがる範囲を自由に飛べるようにする。各辺でクランプ + 速度反転 +
// Restitution 減衰。連続 5 tick 低速で停止、400 tick 安全装置でも強制終了。
func stepThrowIE(s *ActionState, m *Mascot, env *Environment) bool {
	if _, tried := s.CachedParams["_grabInit"]; !tried {
		if !ensureGrabbed(s, m, env) {
			return true
		}
	}

	var vx, vy, wx, wy float64
	if init, _ := s.CachedParams["_throwInit"].(bool); !init {
		s.CachedParams["_throwInit"] = true
		// ウィンドウ位置の初期値 = 現在のグラブ位置 (mascot.Anchor + grabbedOffset)。
		// 以後この (wx, wy) を独自に更新する (= マスコットから切り離して飛ばす)。
		offset := m.GrabbedOffset()
		wx = float64(m.Anchor.X + offset.X)
		wy = float64(m.Anchor.Y + offset.Y)
		if ev, ok := s.Action.Params["InitialVX"]; ok && ev != nil {
			vx, _ = ev.EvalFloat(m.vm, s.CachedParams)
		}
		if ev, ok := s.Action.Params["InitialVY"]; ok && ev != nil {
			vy, _ = ev.EvalFloat(m.vm, s.CachedParams)
		}
	} else {
		vx = toFloat(s.CachedParams["_vx"])
		vy = toFloat(s.CachedParams["_vy"])
		wx = toFloat(s.CachedParams["_wx"])
		wy = toFloat(s.CachedParams["_wy"])
	}

	gravity := 2.0
	if ev, ok := s.Action.Params["Gravity"]; ok && ev != nil {
		gravity, _ = ev.EvalFloat(m.vm, s.CachedParams)
	}
	resX := 0.0
	if ev, ok := s.Action.Params["RegistanceX"]; ok && ev != nil {
		resX, _ = ev.EvalFloat(m.vm, s.CachedParams)
	} else if ev, ok := s.Action.Params["ResistanceX"]; ok && ev != nil {
		resX, _ = ev.EvalFloat(m.vm, s.CachedParams)
	}

	vx *= 1 - resX
	vy += gravity
	wx += vx
	wy += vy

	// 境界 = 全モニタ WorkArea の連合矩形。マルチモニタでも自由に飛べる。
	winW := toInt(s.CachedParams["_winW"])
	winH := toInt(s.CachedParams["_winH"])
	bounds := env.WorkAreaUnion()

	const (
		restitution    = 0.4 // 反射時の減衰 (0.6 だとバウンドがキツすぎる)
		crossFriction  = 0.7 // バウンド時の直交軸減衰 (床着地で X 速度を落とす等)
		bounceMinSpeed = 2.0
	)
	abs := func(f float64) float64 {
		if f < 0 {
			return -f
		}
		return f
	}

	// 左端
	if wx < float64(bounds.Min.X) && vx < 0 {
		wx = float64(bounds.Min.X)
		vx = -vx * restitution
		vy *= crossFriction
	}
	// 右端 (winRight = wx + winW)
	if wx+float64(winW) > float64(bounds.Max.X) && vx > 0 {
		wx = float64(bounds.Max.X - winW)
		vx = -vx * restitution
		vy *= crossFriction
	}
	// 上端
	if wy < float64(bounds.Min.Y) && vy < 0 {
		wy = float64(bounds.Min.Y)
		vy = -vy * restitution
		vx *= crossFriction
	}
	// 下端 (winBottom = wy + winH)
	if wy+float64(winH) > float64(bounds.Max.Y) && vy > 0 {
		wy = float64(bounds.Max.Y - winH)
		vy = -vy * restitution
		vx *= crossFriction
	}

	s.CachedParams["_vx"] = vx
	s.CachedParams["_vy"] = vy
	s.CachedParams["_wx"] = wx
	s.CachedParams["_wy"] = wy

	advanceAnimation(s, m, env, true)

	// ウィンドウを直接移動する。mascot.Anchor は触らないので、マスコットは投げた瞬間の
	// 位置に固定されたまま (Pose Velocity が 0 なら) アニメだけ続ける。
	if m.grabbedHWND == 0 || !platform.IsExternalWindowAlive(m.grabbedHWND) {
		m.clearGrab()
		return true
	}
	if err := platform.MoveExternalWindow(m.grabbedHWND, int(wx), int(wy)); err != nil {
		m.clearGrab()
		return true
	}

	// 停止判定: 連続 5 tick 低速なら静止と見なして完了。
	slowTicks := toInt(s.CachedParams["_slowTicks"])
	if abs(vx) < bounceMinSpeed && abs(vy) < bounceMinSpeed {
		slowTicks++
	} else {
		slowTicks = 0
	}
	s.CachedParams["_slowTicks"] = slowTicks
	if slowTicks >= 5 {
		m.clearGrab()
		return true
	}

	// 安全装置: 長すぎる Throw は強制終了。
	if m.tick-s.StartTick > 400 {
		m.clearGrab()
		return true
	}
	return false
}

func stepLook(s *ActionState, m *Mascot) bool {
	// LookRight パラメータ指定あり → その値で固定。
	// 指定なし → 現在の向きをトグル (本家の Look 仕様)。
	if ev, ok := s.Action.Params["LookRight"]; ok && ev != nil {
		v, err := ev.EvalBool(m.vm, s.CachedParams)
		if err == nil {
			m.LookRight = v
			return true
		}
	}
	m.LookRight = !m.LookRight
	return true
}

func stepOffset(s *ActionState, m *Mascot) bool {
	dx, dy := 0, 0
	if ev, ok := s.Action.Params["X"]; ok && ev != nil {
		dx, _ = ev.EvalInt(m.vm, s.CachedParams)
	}
	if ev, ok := s.Action.Params["Y"]; ok && ev != nil {
		dy, _ = ev.EvalInt(m.vm, s.CachedParams)
	}
	// LookRight=true (右向き表示) のとき X 方向反転 (advanceAnimation と整合)
	if m.LookRight {
		dx = -dx
	}
	m.Anchor.X += dx
	m.Anchor.Y += dy
	return true
}

func stepFalling(s *ActionState, m *Mascot, env *Environment) bool {
	// Thrown / Fall ロール中の Falling は、モニタの左右壁・天井に当たった時だけ
	// 対応する Action (HoldOntoWall / HoldOntoCeiling) へ確定遷移させる。
	// バウンドしてルーレットで HoldOntoWall を引き直しに行く挙動だと、上半分の
	// 壁で Climb 系 (Animation Condition 不満による即時完了) を引いて「立つ・座る」
	// に流れる問題が起きる。確定遷移なら必ず該当 Behavior が起動する。
	//
	// IE 辺 (上面以外) への掴まりは XML 側の経路 (HoldOntoIEWall = ClimbWall) が
	// 完了後の advanceBehavior で FallFromWall を引いてしまう構造的問題があるため
	// 現時点では omit (= 通常の物理: borderTolerance 範囲内に止まらない限り素通り)。
	// IE 上面着地も Thrown/Fall Sequence の Bouncing→Stand 経路に任せて
	// バウンドアニメを再生させる (= grabOnImpact による即時 forcedNext は行わない)。
	grabOnImpact := behaviorMatchesRole(m.CurrentBehavior, "Thrown") ||
		behaviorMatchesRole(m.CurrentBehavior, "Fall")

	// forceGrab は衝突した辺に対応する Behavior を forcedNext にセットし、成否を返す。
	// 該当 Behavior がそのキャラ XML に存在しない場合 (例: 旧日本語版で
	// 名前が「壁につかまる」になっている) は false を返して呼び出し元にフォールバック
	// (= バウンド) させる。これで本家英語版以外のキャラが想定外の挙動を
	// 起こさないようにする。
	forceGrab := func(behaviorName string) bool {
		if _, ok := m.findBehaviorByName(behaviorName); ok {
			m.forcedNext = &BehaviorRef{Name: behaviorName}
			return true
		}
		return false
	}

	// 物理状態は CachedParams に float で保持する (整数だと小速度で精度が失われる)。
	var vx, vy, ax, ay float64
	if init, _ := s.CachedParams["_init"].(bool); !init {
		s.CachedParams["_init"] = true
		ax = float64(m.Anchor.X)
		ay = float64(m.Anchor.Y)
		vx = float64(m.Velocity.X)
		vy = float64(m.Velocity.Y)
		if ev, ok := s.Action.Params["InitialVX"]; ok && ev != nil {
			// XML 側の式 (cursor.dx や ${mascot.lookRight ? 10 : -10}) は
			// 絶対座標系で書かれているので Go 側で LookRight 反転は行わない。
			vx, _ = ev.EvalFloat(m.vm, s.CachedParams)
		}
		if ev, ok := s.Action.Params["InitialVY"]; ok && ev != nil {
			vy, _ = ev.EvalFloat(m.vm, s.CachedParams)
		}
	} else {
		vx = toFloat(s.CachedParams["_vx"])
		vy = toFloat(s.CachedParams["_vy"])
		ax = toFloat(s.CachedParams["_ax"])
		ay = toFloat(s.CachedParams["_ay"])
	}

	gravity := 2.0
	if ev, ok := s.Action.Params["Gravity"]; ok && ev != nil {
		gravity, _ = ev.EvalFloat(m.vm, s.CachedParams)
	}
	resX := 0.0
	resY := 0.0
	// 本家オリジナルの綴り (typo) "Registance*" を優先、正しい綴りもフォールバック。
	if ev, ok := s.Action.Params["RegistanceX"]; ok && ev != nil {
		resX, _ = ev.EvalFloat(m.vm, s.CachedParams)
	} else if ev, ok := s.Action.Params["ResistanceX"]; ok && ev != nil {
		resX, _ = ev.EvalFloat(m.vm, s.CachedParams)
	}
	if ev, ok := s.Action.Params["RegistanceY"]; ok && ev != nil {
		resY, _ = ev.EvalFloat(m.vm, s.CachedParams)
	} else if ev, ok := s.Action.Params["ResistanceY"]; ok && ev != nil {
		resY, _ = ev.EvalFloat(m.vm, s.CachedParams)
	}

	// 本家準拠の運動方程式: v_new = v_old * (1 - resistance) + gravity (Y のみ)
	vx *= 1 - resX
	vy = vy*(1-resY) + gravity
	ax += vx
	ay += vy

	m.Anchor.X = int(ax)
	m.Anchor.Y = int(ay)
	m.Velocity = image.Point{X: int(vx), Y: int(vy)}

	s.CachedParams["_vx"] = vx
	s.CachedParams["_vy"] = vy
	s.CachedParams["_ax"] = ax
	s.CachedParams["_ay"] = ay

	// Duration が指定されていれば、そのフレーム数経過で強制完了する
	// (空中状態のまま次の Action へ遷移するため。例: Falling Duration=N → パラシュート)。
	if ev, ok := s.Action.Params["Duration"]; ok && ev != nil {
		dur, _ := ev.EvalInt(m.vm, s.CachedParams)
		if dur > 0 && m.tick-s.StartTick >= dur {
			return true
		}
	}

	// 境界判定: 衝突時は物理バウンド (反射) を行わず、辺にスナップして即停止する。
	// バウンドのアニメ表現は Thrown/Fall Sequence の Bouncing Action (shime18/shime19)
	// が担当する。物理バウンドは「マスコットが画面端でビヨンビヨン跳ね続ける」違和感を
	// 生むので廃止し、確定遷移先 (HoldOntoWall / HoldOntoCeiling) や Sequence 内部の
	// Bouncing アニメに任せる。
	//
	// FIXME(multi-monitor): メインモニタ運用を前提に常にスナップしている。
	// 本来は隣に別モニタがあれば貫通させてマルチモニタ移動を可能にしたい
	// (env.CurrentMonitor / env.HasMonitorAt を使った probe 判定の実装あり)。
	// ebitengine 剥がし完了後にウィンドウ位置設定を物理座標化してから再度有効にする。

	// activeIE 上面のオーバーシュート検出:
	// vy が大きいと 1 step で activeIE.top を跨ぐ場合がある。Floor() は
	// 現 Y のみで判定するため跨ぎを検知できないので、ここで明示的に
	// 「前 Y < top かつ 現 Y >= top」を見て着地させる。
	// (これがないとマスコットがウィンドウを通り抜けて床に落ちてしまう)
	aw := env.ActiveWindow
	if vy > 0 && aw.Visible &&
		m.Anchor.X >= aw.Rect.Min.X && m.Anchor.X <= aw.Rect.Max.X {
		awTop := aw.Rect.Min.Y
		prevY := int(ay - vy)
		if prevY < awTop && m.Anchor.Y >= awTop {
			m.Anchor.Y = awTop
			m.Velocity = image.Point{}
			return true
		}
	}

	// 床: vy > 0 のときだけ。Floor() が activeIE.top を返すケース (anchor が
	// activeIE 上の X 範囲 & activeIE.top の上にいる) もここでまとめて着地する。
	// 着地後は Thrown/Fall Sequence の Bouncing→Stand を経由してバウンドアニメ再生。
	if vy > 0 && m.Anchor.Y >= env.Floor(m.Anchor) {
		m.Anchor.Y = env.Floor(m.Anchor)
		m.Velocity = image.Point{}
		return true
	}
	// 左壁: 左向きに飛んでいた = LookRight=false → On the Wall Condition
	// `lookRight ? rightBorder : leftBorder` の左壁判定と整合させる。
	if vx < 0 && m.Anchor.X <= env.LeftWall(m.Anchor) {
		m.Anchor.X = env.LeftWall(m.Anchor)
		if grabOnImpact && forceGrab("HoldOntoWall") {
			m.LookRight = false
		}
		m.Velocity = image.Point{}
		return true
	}
	// 右壁
	if vx > 0 && m.Anchor.X >= env.RightWall(m.Anchor) {
		m.Anchor.X = env.RightWall(m.Anchor)
		if grabOnImpact && forceGrab("HoldOntoWall") {
			m.LookRight = true
		}
		m.Velocity = image.Point{}
		return true
	}
	// 天井
	if vy < 0 && m.Anchor.Y <= env.Ceiling(m.Anchor) {
		m.Anchor.Y = env.Ceiling(m.Anchor)
		if grabOnImpact {
			forceGrab("HoldOntoCeiling")
		}
		m.Velocity = image.Point{}
		return true
	}
	// Animation も進める (落下中のポーズ更新、ループ再生)
	advanceAnimation(s, m, env, true)
	return false
}

// stepJumping は Embedded Jump Class の物理運動を実装する。
//
// 本家仕様:
//   - VelocityParam (本家英語版) または 速度 → Velocity (旧日本語版から翻訳された param) を
//     **上向き初速** の大きさとして取る (デフォルト 20)
//   - Gravity (デフォルト 2) で毎 tick 重力加速
//   - TargetX が指定されていれば、滞空時間 (= 2*v0/g) から水平速度 vx を逆算
//   - TargetX 通過 / 壁・天井衝突 / 床着地 / TargetY 通過 / タイムアウト のいずれかで完了
//
// 用例 (旧日本語版「左の壁に歩いて飛びつく」):
//   <ActionReference Name="ジャンプ" 目的地X="${workArea.left}" 目的地Y="${workArea.bottom-rand*height/4}" />
//   → 壁端 X を狙って放物線で飛び、壁にぶつかったら完了 → 次の「壁に掴まる」が壁の位置で開始される。
func stepJumping(s *ActionState, m *Mascot, env *Environment) bool {
	var vx, vy, ax, ay float64

	if init, _ := s.CachedParams["_jinit"].(bool); !init {
		s.CachedParams["_jinit"] = true
		ax = float64(m.Anchor.X)
		ay = float64(m.Anchor.Y)
		s.CachedParams["_startX"] = m.Anchor.X
		s.CachedParams["_startY"] = m.Anchor.Y

		// 初速: VelocityParam (本家英語版) を優先、なければ Velocity (旧日本語版から翻訳された「速度」)。
		// それも無ければ 20。上向きなので符号は負。
		v0 := 20.0
		if ev, ok := s.Action.Params["VelocityParam"]; ok && ev != nil {
			v0, _ = ev.EvalFloat(m.vm, s.CachedParams)
		} else if ev, ok := s.Action.Params["Velocity"]; ok && ev != nil {
			v0, _ = ev.EvalFloat(m.vm, s.CachedParams)
		}
		if v0 <= 0 {
			v0 = 20
		}
		vy = -v0

		gravity := 2.0
		if ev, ok := s.Action.Params["Gravity"]; ok && ev != nil {
			gravity, _ = ev.EvalFloat(m.vm, s.CachedParams)
		}
		if gravity <= 0 {
			gravity = 2
		}
		s.CachedParams["_gravity"] = gravity

		// TargetX から水平速度を逆算。滞空時間 N = 2*v0/g (上昇+下降を等時として近似)。
		// TargetY 指定があっても水平速度の計算は TargetX 主導で十分実用的。
		if ev, ok := s.Action.Params["TargetX"]; ok && ev != nil {
			tx, _ := ev.EvalInt(m.vm, s.CachedParams)
			s.CachedParams["_targetX"] = tx
			flightTicks := 2 * v0 / gravity
			if flightTicks < 1 {
				flightTicks = 1
			}
			vx = (float64(tx) - ax) / flightTicks
			m.LookRight = float64(tx) > ax
		}
		if ev, ok := s.Action.Params["TargetY"]; ok && ev != nil {
			ty, _ := ev.EvalInt(m.vm, s.CachedParams)
			s.CachedParams["_targetY"] = ty
		}
		if m.OnMoveStart != nil {
			t := m.Anchor
			if v, ok := s.CachedParams["_targetX"]; ok {
				t.X = toInt(v)
			}
			if v, ok := s.CachedParams["_targetY"]; ok {
				t.Y = toInt(v)
			}
			m.OnMoveStart(s.Action.Name, t, m.Anchor)
		}
	} else {
		vx = toFloat(s.CachedParams["_vx"])
		vy = toFloat(s.CachedParams["_vy"])
		ax = toFloat(s.CachedParams["_ax"])
		ay = toFloat(s.CachedParams["_ay"])
	}

	gravity := toFloat(s.CachedParams["_gravity"])
	if gravity <= 0 {
		gravity = 2
	}

	// 物理運動: 重力で vy を増加 → 位置に積分
	vy += gravity
	ax += vx
	ay += vy
	m.Anchor.X = int(ax)
	m.Anchor.Y = int(ay)
	m.Velocity = image.Point{X: int(vx), Y: int(vy)}

	s.CachedParams["_vx"] = vx
	s.CachedParams["_vy"] = vy
	s.CachedParams["_ax"] = ax
	s.CachedParams["_ay"] = ay

	advanceAnimation(s, m, env, true)

	// 完了判定:
	// 1. TargetX 通過 (進行方向側で startX を越えた)
	if v, ok := s.CachedParams["_targetX"]; ok {
		tx := toInt(v)
		startX := toInt(s.CachedParams["_startX"])
		if startX < tx && m.Anchor.X >= tx {
			return true
		}
		if startX > tx && m.Anchor.X <= tx {
			return true
		}
	}
	// 2. 壁・天井衝突
	if vx < 0 && m.Anchor.X <= env.LeftWall(m.Anchor) {
		return true
	}
	if vx > 0 && m.Anchor.X >= env.RightWall(m.Anchor) {
		return true
	}
	if vy < 0 && m.Anchor.Y <= env.Ceiling(m.Anchor) {
		return true
	}
	// 3. TargetY 通過 (放物線の頂点近くで命中させたい用途)
	if v, ok := s.CachedParams["_targetY"]; ok {
		ty := toInt(v)
		startY := toInt(s.CachedParams["_startY"])
		if startY > ty && m.Anchor.Y <= ty {
			return true
		}
		if startY < ty && m.Anchor.Y >= ty {
			return true
		}
	}
	// 4. 床着地 (Target 未到達でも落ち着いたら抜ける)
	if vy > 0 && m.Anchor.Y >= env.Floor(m.Anchor) {
		return true
	}
	// 5. 安全装置 (無限ループ防止)
	if m.tick-s.StartTick > 200 {
		return true
	}
	return false
}

// stepBroadcast は Broadcast Action を 1 tick 進める。
//
// 役割: 自分の Affordance 文字列を Registry に公示し、ScanMove 持ちの相手が
// 到着するか、アニメーションが完走するか、BorderType の境界を外れるまで
// 同じポーズで待ち続ける。到着があった場合は ScanMove 側が指定した
// TargetBehavior を forcedNext にセットして終了する。
func stepBroadcast(s *ActionState, m *Mascot, env *Environment) bool {
	if m.registry == nil || s.Action.Affordance == "" {
		// レジストリ無効 or Affordance 未指定 → no-op で完走させる
		return advanceAnimation(s, m, env, false)
	}

	entry, _ := s.CachedParams["_entry"].(*BroadcastEntry)
	if entry == nil {
		entry = m.registry.Register(s.Action.Affordance, m, m.Anchor)
		s.CachedParams["_entry"] = entry
		if m.OnAffordance != nil {
			m.OnAffordance("broadcast-start", s.Action.Affordance, m.Name)
		}
	}
	entry.Position = m.Anchor

	// 到着済み → 相手 ScanMove が指定した TargetBehavior を強制遷移キューに積む
	if entry.Arrived {
		if entry.TargetBehavior != "" {
			m.forcedNext = &BehaviorRef{Name: entry.TargetBehavior}
		}
		if m.OnAffordance != nil {
			m.OnAffordance("broadcast-arrived", s.Action.Affordance, "→ "+entry.TargetBehavior)
		}
		m.registry.Unregister(entry)
		return true
	}

	// BorderType="Floor" 外しは早期終了 (元 stepMove と同方針)
	switch s.Action.BorderType {
	case "Floor":
		if !env.IsOnFloor(m.Anchor) {
			if m.OnAffordance != nil {
				m.OnAffordance("broadcast-border-lost", s.Action.Affordance, "Floor")
			}
			m.registry.Unregister(entry)
			return true
		}
	case "Wall":
		if !env.IsOnWall(m.Anchor) {
			if m.OnAffordance != nil {
				m.OnAffordance("broadcast-border-lost", s.Action.Affordance, "Wall")
			}
			m.registry.Unregister(entry)
			return true
		}
	case "Ceiling":
		if !env.IsOnCeiling(m.Anchor) {
			if m.OnAffordance != nil {
				m.OnAffordance("broadcast-border-lost", s.Action.Affordance, "Ceiling")
			}
			m.registry.Unregister(entry)
			return true
		}
	}

	// アニメーション再生 (loop=false → 全 Pose 完走で終了)
	if advanceAnimation(s, m, env, false) {
		// アニメ完走 = 規定時間 (例 Duration=250 ≒ 10s) 待っても誰も来なかった = タイムアウト
		if m.OnAffordance != nil {
			m.OnAffordance("broadcast-timeout", s.Action.Affordance, "animation completed, no scanmove arrived")
		}
		m.registry.Unregister(entry)
		return true
	}
	return false
}

// stepScanMove は ScanMove Action を 1 tick 進める。
//
// 役割: 同じ Affordance を Broadcast 中の Mascot を Registry から探し、
// その座標へ向かって歩く。X 方向通過で到着判定。到着時に双方の forcedNext を
// セットして終了する。候補が消えていれば失敗終了。
func stepScanMove(s *ActionState, m *Mascot, env *Environment) bool {
	if m.registry == nil || s.Action.Affordance == "" {
		return true
	}

	target, _ := s.CachedParams["_target"].(*BroadcastEntry)
	if target == nil {
		target = m.registry.FindNearest(s.Action.Affordance, m.Anchor, m)
		if target == nil {
			// 候補なし → 即終了 (NextBehavior 通常抽選へ)
			if m.OnAffordance != nil {
				m.OnAffordance("scanmove-no-target", s.Action.Affordance, m.Name)
			}
			return true
		}
		s.CachedParams["_target"] = target
		// 進行方向を決定
		switch {
		case target.Position.X > m.Anchor.X:
			m.LookRight = true
			s.CachedParams["_dirX"] = 1
		case target.Position.X < m.Anchor.X:
			m.LookRight = false
			s.CachedParams["_dirX"] = -1
		default:
			s.CachedParams["_dirX"] = 0
		}
		if m.OnAffordance != nil {
			// 距離 (Manhattan) を含めて成立しやすさを可視化する。
			// ScanMove の歩行速度 × Broadcast 残り時間と比較すれば「届く可能性」が見える。
			distance := abs(target.Position.X-m.Anchor.X) + abs(target.Position.Y-m.Anchor.Y)
			detail := fmt.Sprintf("%s → ?, dist=%d", m.Name, distance)
			if target.Mascot != nil {
				detail = fmt.Sprintf("%s → %s, dist=%d", m.Name, target.Mascot.Name, distance)
			}
			m.OnAffordance("scanmove-found", s.Action.Affordance, detail)
		}
		if m.OnMoveStart != nil {
			m.OnMoveStart(s.Action.Name, target.Position, m.Anchor)
		}
	}

	// target が unregister された (broadcaster が中断・到着済み他者勝ち) → 失敗終了
	if target.Cancelled || target.Arrived {
		if m.OnAffordance != nil {
			reason := "target unregistered"
			if target.Arrived {
				reason = "another mascot arrived first"
			}
			m.OnAffordance("scanmove-failed", s.Action.Affordance, reason)
		}
		return true
	}

	// アニメーションループ再生 (Velocity で前進)
	advanceAnimation(s, m, env, true)

	// X 方向通過判定 (stepMove と同方式)
	dirX, _ := s.CachedParams["_dirX"].(int)
	arrived := false
	switch {
	case dirX == 0:
		arrived = true
	case dirX > 0 && m.Anchor.X >= target.Position.X:
		arrived = true
	case dirX < 0 && m.Anchor.X <= target.Position.X:
		arrived = true
	}

	if arrived {
		// 双方の Behavior 強制遷移を確定
		target.Arrived = true
		target.TargetBehavior = s.Action.TargetBehaviorAttr
		if s.Action.BehaviorAttr != "" {
			m.forcedNext = &BehaviorRef{Name: s.Action.BehaviorAttr}
		}
		if m.OnAffordance != nil {
			detail := m.Name + " → " + s.Action.BehaviorAttr
			if target.Mascot != nil {
				detail += " / " + target.Mascot.Name + " → " + s.Action.TargetBehaviorAttr
			}
			m.OnAffordance("scanmove-arrived", s.Action.Affordance, detail)
		}
		return true
	}

	// BorderType="Floor" 外しは失敗終了
	switch s.Action.BorderType {
	case "Floor":
		if !env.IsOnFloor(m.Anchor) {
			if m.OnAffordance != nil {
				m.OnAffordance("scanmove-border-lost", s.Action.Affordance, "Floor")
			}
			return true
		}
	case "Wall":
		if !env.IsOnWall(m.Anchor) {
			if m.OnAffordance != nil {
				m.OnAffordance("scanmove-border-lost", s.Action.Affordance, "Wall")
			}
			return true
		}
	case "Ceiling":
		if !env.IsOnCeiling(m.Anchor) {
			if m.OnAffordance != nil {
				m.OnAffordance("scanmove-border-lost", s.Action.Affordance, "Ceiling")
			}
			return true
		}
	}
	return false
}

func stepDragged(s *ActionState, m *Mascot) bool {
	if m.Drag != DragHolding {
		// 離されたらこの Action は終了 (割り込みで Thrown に行く)
		return true
	}
	// ドラッグ開始時のオフセットを維持したまま、カーソル位置に直接追従する。
	m.Anchor.X = m.env.Cursor.X + m.dragOffset.X
	m.Anchor.Y = m.env.Cursor.Y + m.dragOffset.Y

	// Animation はループ再生 (Pose 切替は時間駆動)
	if anim := pickAnimation(s, m); anim != nil && len(anim.Poses) > 0 {
		pose := anim.Poses[s.PoseIndex%len(anim.Poses)]
		s.PoseTick++
		if s.PoseTick >= pose.Duration {
			s.PoseTick = 0
			s.PoseIndex++
		}
	}
	return false
}

// stepBreed は Breed Action を 1 tick 進める。
//
// 役割: 自分のアニメーションを最後まで再生し、完了時に新 Mascot 生成リクエストを
// Spawner に投げる。totalCount のチェックは Behavior の Condition 側で済んでいる
// 前提なので stepBreed では再チェックしない (抽選を通った時点で生成可)。
//
// BornX は LookRight=true なら反転 (Pose.Velocity と同じ規約)。
// Spawner 未設定 (テスト等) なら no-op で受け流す。
func stepBreed(s *ActionState, m *Mascot, env *Environment) bool {
	// アニメーション再生 (loop=false → 完走で終了)
	if !advanceAnimation(s, m, env, false) {
		return false
	}

	// 二重生成防止: アニメ完走後の最終 tick で 1 回だけ Spawn を発火させる。
	// advanceAnimation は完走後も true を返し続けるが、stepBreed は完走 tick で
	// return true するので二重呼び出しは構造的に発生しない。

	if m.spawner == nil {
		return true // Spawner 無し (テスト・単独起動) → 静かに受け流す
	}

	bornX := 0
	if ev, ok := s.Action.Params["BornX"]; ok && ev != nil {
		bornX, _ = ev.EvalInt(m.vm, s.CachedParams)
	}
	bornY := 0
	if ev, ok := s.Action.Params["BornY"]; ok && ev != nil {
		bornY, _ = ev.EvalInt(m.vm, s.CachedParams)
	}
	// LookRight=true → BornX 反転 (Pose.Velocity 規約と整合)。
	// 例: BornX=-32 は「親の背中側に出す」意図 → 親が右向き時は +32 にミラー。
	if m.LookRight {
		bornX = -bornX
	}

	behavior := s.Action.BornBehavior
	if behavior == "" {
		log.Printf("warning: action %q is Breed but BornBehavior empty", s.Action.Name)
		return true
	}

	m.spawner.Spawn(SpawnRequest{
		ParentName:      m.Name,
		Anchor:          image.Point{X: m.Anchor.X + bornX, Y: m.Anchor.Y + bornY},
		LookRight:       m.LookRight,
		InitialBehavior: behavior,
	})
	return true
}
