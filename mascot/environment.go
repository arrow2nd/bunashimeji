package mascot

import (
	"image"

	"github.com/arrow2nd/bunashimeji/platform"
)

// 床/壁の判定許容ピクセル
const borderTolerance = 2

type Environment struct {
	// Screens は WorkArea のみのリスト (template.go の topmostScreens 等で参照)。
	Screens []image.Rectangle
	// screens はモニタ全体 + WorkArea のペア。物理判定 (含むかどうか) は Monitor で、
	// 床/壁の高さは WorkArea で行うことで、モニタ間境界の隙間を埋める。
	screens     []platform.ScreenInfo
	Cursor      image.Point
	CursorDelta image.Point // 前 tick との差分 (Thrown の初速に使用)

	// ActiveWindow はホワイトリスト合致した現在のフォアグラウンドウィンドウ。
	// 本家の `mascot.environment.activeIE` に対応する。
	// Visible=false の場合、JS 側からも .visible=false で見える。
	ActiveWindow platform.ActiveWindow

	prevCursor image.Point
	hasPrev    bool
}

// Refresh は platform 層から最新の情報を取り込む。
func (e *Environment) Refresh() {
	e.screens = platform.Screens()
	e.Screens = e.Screens[:0]
	for _, s := range e.screens {
		e.Screens = append(e.Screens, s.WorkArea)
	}
	cur := platform.CursorPosition()
	if e.hasPrev {
		e.CursorDelta = image.Point{X: cur.X - e.prevCursor.X, Y: cur.Y - e.prevCursor.Y}
	}
	e.prevCursor = cur
	e.hasPrev = true
	e.Cursor = cur
	e.ActiveWindow = platform.GetActiveWindow()
}

// currentIndex は anchor を含むモニタのインデックスを返す。
// 含むモニタが無ければ最近傍を選ぶ。物理範囲 (Monitor) で判定するので
// WorkArea の隙間 (タスクバー領域等) があっても安定して判定できる。
func (e *Environment) currentIndex(anchor image.Point) int {
	if len(e.screens) == 0 {
		return -1
	}
	for i, s := range e.screens {
		if anchor.In(s.Monitor) {
			return i
		}
	}
	bestIdx := 0
	bestDist := rectDistanceSq(anchor, e.screens[0].Monitor)
	for i := 1; i < len(e.screens); i++ {
		d := rectDistanceSq(anchor, e.screens[i].Monitor)
		if d < bestDist {
			bestIdx = i
			bestDist = d
		}
	}
	return bestIdx
}

// CurrentScreen は anchor を含むスクリーン (= WorkArea) を返す。
// 含まれない場合は最近傍モニタの WorkArea。
func (e *Environment) CurrentScreen(anchor image.Point) image.Rectangle {
	idx := e.currentIndex(anchor)
	if idx < 0 {
		return image.Rect(0, 0, 1920, 1080)
	}
	return e.screens[idx].WorkArea
}

// CurrentMonitor は anchor を含むモニタの物理範囲を返す。
// 貫通判定 (隣モニタ探索) で使う。
func (e *Environment) CurrentMonitor(anchor image.Point) image.Rectangle {
	idx := e.currentIndex(anchor)
	if idx < 0 {
		return image.Rect(0, 0, 1920, 1080)
	}
	return e.screens[idx].Monitor
}

// WorkAreaUnion は全モニタの WorkArea を包含する最小矩形を返す。
// ThrowIE のバウンド境界として「複数モニタにまたがる有効範囲」を使うために用意。
// シングルモニタ環境では CurrentScreen と同じ結果になる。
// 異なる Y 範囲のモニタ (4K + FHD 等) では「隙間」もこの矩形に含まれるが、
// バウンド境界としては最外周で許容する。
func (e *Environment) WorkAreaUnion() image.Rectangle {
	if len(e.Screens) == 0 {
		return image.Rect(0, 0, 1920, 1080)
	}
	u := e.Screens[0]
	for _, s := range e.Screens[1:] {
		u = u.Union(s)
	}
	return u
}

// HasMonitorAt は (x, y) を含むモニタ (物理範囲) があれば true。
// 投擲時のマルチモニター貫通判定に使う。WorkArea ではなく Monitor を見ることで
// 隙間 (タスクバー位置等) で誤判定しない。
func (e *Environment) HasMonitorAt(p image.Point) bool {
	for _, s := range e.screens {
		if p.In(s.Monitor) {
			return true
		}
	}
	return false
}

// validPositionTolerance は IsAtValidPosition の判定マージン (px)。
// 着地時 anchor.Y = WorkArea.Max.Y は Monitor.Max.Y を上回り得ない一方、
// Go の image.Rectangle は Max を排他境界とするため、境界 Y が
// "ぎりぎり外" にならないようにこの tolerance を足す。
const validPositionTolerance = 4

// IsAtValidPosition は anchor がマスコットとして有効な位置にいるか判定する。
// 「いずれかのモニタの X 範囲内 (±tol) かつ Y がそのモニタ下端 +tol 以下」を有効とする。
// 初回 spawn 時の上空 (Y = Min.Y - 200) や、着地後の Y = Max.Y も有効側に含まれる。
// false なら anchor がどのモニタの近傍にも存在しない異常な座標なので、リスポーン対象。
func (e *Environment) IsAtValidPosition(p image.Point) bool {
	for _, s := range e.screens {
		m := s.Monitor
		if p.X < m.Min.X-validPositionTolerance || p.X > m.Max.X+validPositionTolerance {
			continue
		}
		if p.Y <= m.Max.Y+validPositionTolerance {
			return true
		}
	}
	return false
}

// rectDistanceSq は p から rect までの最短距離の二乗を返す (p が rect 内なら 0)。
func rectDistanceSq(p image.Point, r image.Rectangle) int {
	dx := 0
	if p.X < r.Min.X {
		dx = r.Min.X - p.X
	} else if p.X >= r.Max.X {
		dx = p.X - r.Max.X + 1
	}
	dy := 0
	if p.Y < r.Min.Y {
		dy = r.Min.Y - p.Y
	} else if p.Y >= r.Max.Y {
		dy = p.Y - r.Max.Y + 1
	}
	return dx*dx + dy*dy
}

// Floor は anchor の真下にある最も近い床面 Y を返す。
//
// 通常は画面下端 (CurrentScreen.Max.Y) だが、activeIE がホワイトリスト合致して
// 表示中で、anchor が activeIE の X 範囲内かつ activeIE 上面より上にいる場合は、
// 「より近い床」として activeIE.Top を返す (= ウィンドウ上に着地する)。
//
// stepFalling はこの値で着地判定するので、これだけで「ウィンドウ上に乗る」が成立する
// (vy が大きい時のオーバーシュートは stepFalling 側で別途対策)。
func (e *Environment) Floor(anchor image.Point) int {
	screenFloor := e.CurrentScreen(anchor).Max.Y
	aw := e.ActiveWindow
	if aw.Visible &&
		anchor.X >= aw.Rect.Min.X && anchor.X <= aw.Rect.Max.X &&
		anchor.Y <= aw.Rect.Min.Y &&
		aw.Rect.Min.Y < screenFloor {
		return aw.Rect.Min.Y
	}
	return screenFloor
}

// Ceiling は anchor が含まれるスクリーンの上端 Y を返す。
func (e *Environment) Ceiling(anchor image.Point) int {
	return e.CurrentScreen(anchor).Min.Y
}

// LeftWall / RightWall は左右端 X を返す。
func (e *Environment) LeftWall(anchor image.Point) int  { return e.CurrentScreen(anchor).Min.X }
func (e *Environment) RightWall(anchor image.Point) int { return e.CurrentScreen(anchor).Max.X }

// IsOnFloor は anchor が「歩ける水平面」に接しているか判定する。
// 画面床面 OR activeIE 上面 (X 範囲内) のどちらかに接していれば true。
func (e *Environment) IsOnFloor(anchor image.Point) bool {
	if abs(anchor.Y-e.CurrentScreen(anchor).Max.Y) <= borderTolerance {
		return true
	}
	return e.IsOnActiveWindow(anchor)
}

// IsOnActiveWindow は anchor が activeIE の上面に乗っているか判定する。
// X が activeIE 範囲内かつ Y が上面と接触許容内なら true。
// IsOnFloor (歩行可能水平面) に組み込まれており、上面に「乗って歩ける」かどうかの判定に使う。
func (e *Environment) IsOnActiveWindow(anchor image.Point) bool {
	aw := e.ActiveWindow
	if !aw.Visible {
		return false
	}
	return anchor.X >= aw.Rect.Min.X && anchor.X <= aw.Rect.Max.X &&
		abs(anchor.Y-aw.Rect.Min.Y) <= borderTolerance
}

// IsAttachedToActiveWindow は anchor が activeIE のいずれかの辺
// (top / bottom / left / right) に接しているか判定する。
// ウィンドウ追従 (Mascot.followActiveWindowIfNeeded) はこの判定で
// 「ウィンドウにくっつき続けるべきか」を決める。上面に乗る場合だけでなく、
// JumpOnIELeftWall などで側面/底面にしがみついている場合もウィンドウ移動に
// 追従させるため、4 辺すべてを対象にする。
func (e *Environment) IsAttachedToActiveWindow(anchor image.Point) bool {
	aw := e.ActiveWindow
	if !aw.Visible {
		return false
	}
	r := aw.Rect
	// 上面/底面: X 範囲内かつ Y が top または bottom と接触
	if anchor.X >= r.Min.X && anchor.X <= r.Max.X {
		if abs(anchor.Y-r.Min.Y) <= borderTolerance {
			return true
		}
		if abs(anchor.Y-r.Max.Y) <= borderTolerance {
			return true
		}
	}
	// 左壁/右壁: Y 範囲内かつ X が left または right と接触
	if anchor.Y >= r.Min.Y && anchor.Y <= r.Max.Y {
		if abs(anchor.X-r.Min.X) <= borderTolerance {
			return true
		}
		if abs(anchor.X-r.Max.X) <= borderTolerance {
			return true
		}
	}
	return false
}

// IsOnCeiling は anchor.Y が天井に接しているか判定する。
func (e *Environment) IsOnCeiling(anchor image.Point) bool {
	return abs(anchor.Y-e.Ceiling(anchor)) <= borderTolerance
}

// IsOnWall は anchor.X が左右端に接しているか判定する。
// マルチモニタ環境では、隣に別モニタの WorkArea が続いている辺は「壁ではない」
// (マスコットは渡って歩ける) ので false を返す。タスクバー越しのモニタ間は WorkArea
// が連続しないため壁扱い (= 渡れない) のままになる。
func (e *Environment) IsOnWall(anchor image.Point) bool {
	leftHit := abs(anchor.X-e.LeftWall(anchor)) <= borderTolerance
	rightHit := abs(anchor.X-e.RightWall(anchor)) <= borderTolerance
	if leftHit && !e.hasAdjacentScreen(anchor, true) {
		return true
	}
	if rightHit && !e.hasAdjacentScreen(anchor, false) {
		return true
	}
	return false
}

// hasAdjacentScreen は anchor 所属モニタの左 (leftSide=true) または右の隣に、
// 別モニタの WorkArea が接続されているかを返す。Monitor 物理範囲ではなく WorkArea を
// プローブ先にすることで、タスクバーがモニタ間に挟まる構成では「隣接無し」(= 真の壁)
// と判定する。マスコットが歩けるのは WorkArea 上だけなので、これが「渡れる隣」の定義。
func (e *Environment) hasAdjacentScreen(anchor image.Point, leftSide bool) bool {
	cur := e.currentIndex(anchor)
	if cur < 0 {
		return false
	}
	wa := e.screens[cur].WorkArea
	var probe image.Point
	if leftSide {
		probe = image.Point{X: wa.Min.X - 1, Y: anchor.Y}
	} else {
		// image.Rectangle の Max は排他的なので、wa.Max.X 自体が「1 px 外」相当。
		probe = image.Point{X: wa.Max.X, Y: anchor.Y}
	}
	for i, s := range e.screens {
		if i == cur {
			continue
		}
		if probe.In(s.WorkArea) {
			return true
		}
	}
	return false
}

// pointFromAny は goja から渡される anchor (map または mascot.anchor 全体) を image.Point 化する。
// jsScratch の isOn クロージャが anchor 引数を受けるときに使う。
func pointFromAny(v any) image.Point {
	if v == nil {
		return image.Point{}
	}
	if m, ok := v.(map[string]any); ok {
		return image.Point{X: toInt(m["x"]), Y: toInt(m["y"])}
	}
	// goja は map[string]interface{} 以外に Object 経由でも渡せるが、
	// jsScratch で渡す anchor はすべて map[string]any なのでこれで十分。
	return image.Point{}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
