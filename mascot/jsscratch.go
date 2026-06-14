package mascot

import (
	"image"
)

// jsScratch は goja に渡す `mascot` ルートオブジェクトを使い回すための保持構造。
//
// 旧実装の BuildJSObject は毎 tick / 個体ごとに map[string]any を十数個と
// `isOn(p any) bool` クロージャを大量にアロケートしていた。これは 100 体 × 25 TPS で
// 秒間 2500 回走り、GC 圧を著しく上げる。
//
// jsScratch は構築時に map とクロージャを 1 度だけ作り、tick ごとに update() で
// 値フィールドのみ書き換える。クロージャは scratch ポインタを通して最新値を参照する
// (例: scratch.screen.Min.X) ため、再生成不要。値の int → any への boxing は
// 避けがたいが、map 自身のアロケと closure オブジェクトのアロケは消える。
type jsScratch struct {
	// 現在値 (closure からは scratch ポインタ経由で読まれる)
	screen        image.Rectangle
	floorY        int
	ceilingY      int
	leftWallX     int
	rightWallX    int
	// leftWallReal / rightWallReal は「その辺が本当の壁か」(隣に WorkArea が続いてい
	// ないか) を保持する。マルチモニタ境界では false になり、XML 側の
	// workArea.leftBorder.isOn / wall.left.isOn 等が壁判定を抑止する。
	leftWallReal  bool
	rightWallReal bool
	activeIE      image.Rectangle
	activeVisible bool

	// goja に渡すルート map。tick ごとに値だけ書き換える。
	root        map[string]any
	anchor      map[string]any
	cursor      map[string]any
	env         map[string]any
	screenMap   map[string]any
	floorMap    map[string]any
	ceilMap     map[string]any
	leftMap     map[string]any
	rightMap    map[string]any
	wallMap     map[string]any
	activeIEMap map[string]any

	// screen (workArea) の border 群
	screenLeft   map[string]any
	screenRight  map[string]any
	screenTop    map[string]any
	screenBottom map[string]any
	screenCenter map[string]any

	// activeIE の border 群
	aieTop    map[string]any
	aieBottom map[string]any
	aieLeft   map[string]any
	aieRight  map[string]any
}

// newJSScratch は scratch を初期化する。map / closure は構築時 1 度だけ作る。
func newJSScratch() *jsScratch {
	s := &jsScratch{}

	s.anchor = map[string]any{"x": 0, "y": 0}
	s.cursor = map[string]any{"x": 0, "y": 0, "dx": 0, "dy": 0}

	// --- workArea/screen の borders ---
	// leftBorder/rightBorder.isOn はマルチモニタ境界 (隣に WorkArea が続いている辺) を
	// 壁としてカウントしない。これがないと「On the Wall」条件がモニタ境界で誤発火し、
	// 存在しない壁に貼りつく挙動になる。
	s.screenLeft = map[string]any{
		"value": 0, "x": 0,
		"isOn": func(p any) bool {
			if !s.leftWallReal {
				return false
			}
			pt := pointFromAny(p)
			return abs(pt.X-s.screen.Min.X) <= borderTolerance
		},
	}
	s.screenRight = map[string]any{
		"value": 0, "x": 0,
		"isOn": func(p any) bool {
			if !s.rightWallReal {
				return false
			}
			pt := pointFromAny(p)
			return abs(pt.X-s.screen.Max.X) <= borderTolerance
		},
	}
	s.screenTop = map[string]any{
		"value": 0, "y": 0,
		"isOn": func(p any) bool {
			pt := pointFromAny(p)
			return abs(pt.Y-s.screen.Min.Y) <= borderTolerance
		},
	}
	s.screenBottom = map[string]any{
		"value": 0, "y": 0,
		"isOn": func(p any) bool {
			pt := pointFromAny(p)
			return abs(pt.Y-s.screen.Max.Y) <= borderTolerance
		},
	}
	s.screenCenter = map[string]any{"x": 0, "y": 0}

	s.screenMap = map[string]any{
		"left": 0, "top": 0, "right": 0, "bottom": 0,
		"width": 0, "height": 0,
		"center":       s.screenCenter,
		"leftBorder":   s.screenLeft,
		"rightBorder":  s.screenRight,
		"topBorder":    s.screenTop,
		"bottomBorder": s.screenBottom,
	}

	// --- floor / ceiling / walls ---
	s.floorMap = map[string]any{
		"value": 0, "y": 0, "border": 0,
		"isOn": func(p any) bool {
			pt := pointFromAny(p)
			return abs(pt.Y-s.floorY) <= borderTolerance
		},
	}
	s.ceilMap = map[string]any{
		"value": 0, "y": 0, "border": 0,
		"isOn": func(p any) bool {
			pt := pointFromAny(p)
			return abs(pt.Y-s.ceilingY) <= borderTolerance
		},
	}
	s.leftMap = map[string]any{
		"value": 0, "x": 0,
		"isOn": func(p any) bool {
			if !s.leftWallReal {
				return false
			}
			pt := pointFromAny(p)
			return abs(pt.X-s.leftWallX) <= borderTolerance
		},
	}
	s.rightMap = map[string]any{
		"value": 0, "x": 0,
		"isOn": func(p any) bool {
			if !s.rightWallReal {
				return false
			}
			pt := pointFromAny(p)
			return abs(pt.X-s.rightWallX) <= borderTolerance
		},
	}
	s.wallMap = map[string]any{
		"left":  s.leftMap,
		"right": s.rightMap,
		"isOn": func(p any) bool {
			pt := pointFromAny(p)
			if s.leftWallReal && abs(pt.X-s.leftWallX) <= borderTolerance {
				return true
			}
			if s.rightWallReal && abs(pt.X-s.rightWallX) <= borderTolerance {
				return true
			}
			return false
		},
	}

	// --- activeIE borders ---
	// isOn は activeVisible=false なら常に false を返す。これにより XML 側の
	// border isOn() 判定で「activeIE が見えていない場合に偶発的 true」を防ぐ。
	s.aieTop = map[string]any{
		"value": 0, "y": 0,
		"isOn": func(p any) bool {
			if !s.activeVisible {
				return false
			}
			pt := pointFromAny(p)
			return abs(pt.Y-s.activeIE.Min.Y) <= borderTolerance &&
				pt.X >= s.activeIE.Min.X && pt.X <= s.activeIE.Max.X
		},
	}
	s.aieBottom = map[string]any{
		"value": 0, "y": 0,
		"isOn": func(p any) bool {
			if !s.activeVisible {
				return false
			}
			pt := pointFromAny(p)
			return abs(pt.Y-s.activeIE.Max.Y) <= borderTolerance &&
				pt.X >= s.activeIE.Min.X && pt.X <= s.activeIE.Max.X
		},
	}
	s.aieLeft = map[string]any{
		"value": 0, "x": 0,
		"isOn": func(p any) bool {
			if !s.activeVisible {
				return false
			}
			pt := pointFromAny(p)
			return abs(pt.X-s.activeIE.Min.X) <= borderTolerance &&
				pt.Y >= s.activeIE.Min.Y && pt.Y <= s.activeIE.Max.Y
		},
	}
	s.aieRight = map[string]any{
		"value": 0, "x": 0,
		"isOn": func(p any) bool {
			if !s.activeVisible {
				return false
			}
			pt := pointFromAny(p)
			return abs(pt.X-s.activeIE.Max.X) <= borderTolerance &&
				pt.Y >= s.activeIE.Min.Y && pt.Y <= s.activeIE.Max.Y
		},
	}

	s.activeIEMap = map[string]any{
		"topBorder":    s.aieTop,
		"bottomBorder": s.aieBottom,
		"leftBorder":   s.aieLeft,
		"rightBorder":  s.aieRight,
		"visible":      false,
		"isVisible":    false,
		"left":         0, "top": 0, "right": 0, "bottom": 0,
		"width": 0, "height": 0,
	}

	s.env = map[string]any{
		"screen":    s.screenMap,
		"workArea":  s.screenMap,
		"cursor":    s.cursor,
		"floor":     s.floorMap,
		"ceiling":   s.ceilMap,
		"wall":      s.wallMap,
		"leftWall":  s.leftMap,
		"rightWall": s.rightMap,
		"activeIE":  s.activeIEMap,
	}

	s.root = map[string]any{
		"anchor":      s.anchor,
		"lookRight":   false,
		"totalCount":  0,
		"environment": s.env,
	}

	return s
}

// update は scratch の数値フィールドを最新の Mascot / Environment 状態で上書きする。
// map のキーはすべて初期化時に作成済みなので、ここでの代入は内部ハッシュ拡張を伴わない。
func (s *jsScratch) update(m *Mascot, env *Environment, totalCount int) {
	s.screen = env.CurrentScreen(m.Anchor)
	s.floorY = env.Floor(m.Anchor)
	s.ceilingY = env.Ceiling(m.Anchor)
	s.leftWallX = s.screen.Min.X
	s.rightWallX = s.screen.Max.X
	// マルチモニタ境界 (隣に別 WorkArea が続く辺) は壁ではないので isOn を抑止する。
	s.leftWallReal = !env.hasAdjacentScreen(m.Anchor, true)
	s.rightWallReal = !env.hasAdjacentScreen(m.Anchor, false)

	// anchor
	s.anchor["x"] = m.Anchor.X
	s.anchor["y"] = m.Anchor.Y

	// cursor
	s.cursor["x"] = env.Cursor.X
	s.cursor["y"] = env.Cursor.Y
	s.cursor["dx"] = env.CursorDelta.X
	s.cursor["dy"] = env.CursorDelta.Y

	// screen / workArea
	scr := s.screen
	w, h := scr.Dx(), scr.Dy()
	s.screenMap["left"] = scr.Min.X
	s.screenMap["top"] = scr.Min.Y
	s.screenMap["right"] = scr.Max.X
	s.screenMap["bottom"] = scr.Max.Y
	s.screenMap["width"] = w
	s.screenMap["height"] = h
	s.screenCenter["x"] = scr.Min.X + w/2
	s.screenCenter["y"] = scr.Min.Y + h/2

	s.screenLeft["value"] = scr.Min.X
	s.screenLeft["x"] = scr.Min.X
	s.screenRight["value"] = scr.Max.X
	s.screenRight["x"] = scr.Max.X
	s.screenTop["value"] = scr.Min.Y
	s.screenTop["y"] = scr.Min.Y
	s.screenBottom["value"] = scr.Max.Y
	s.screenBottom["y"] = scr.Max.Y

	// floor / ceiling
	s.floorMap["value"] = s.floorY
	s.floorMap["y"] = s.floorY
	s.floorMap["border"] = s.floorY
	s.ceilMap["value"] = s.ceilingY
	s.ceilMap["y"] = s.ceilingY
	s.ceilMap["border"] = s.ceilingY

	// walls
	s.leftMap["value"] = s.leftWallX
	s.leftMap["x"] = s.leftWallX
	s.rightMap["value"] = s.rightWallX
	s.rightMap["x"] = s.rightWallX

	// activeIE
	aw := env.ActiveWindow
	s.activeVisible = aw.Visible
	if aw.Visible {
		s.activeIE = aw.Rect
		r := aw.Rect
		s.activeIEMap["visible"] = true
		s.activeIEMap["isVisible"] = true
		s.activeIEMap["left"] = r.Min.X
		s.activeIEMap["top"] = r.Min.Y
		s.activeIEMap["right"] = r.Max.X
		s.activeIEMap["bottom"] = r.Max.Y
		s.activeIEMap["width"] = r.Dx()
		s.activeIEMap["height"] = r.Dy()
		s.aieTop["value"] = r.Min.Y
		s.aieTop["y"] = r.Min.Y
		s.aieBottom["value"] = r.Max.Y
		s.aieBottom["y"] = r.Max.Y
		s.aieLeft["value"] = r.Min.X
		s.aieLeft["x"] = r.Min.X
		s.aieRight["value"] = r.Max.X
		s.aieRight["x"] = r.Max.X
	} else {
		s.activeIE = image.Rectangle{}
		s.activeIEMap["visible"] = false
		s.activeIEMap["isVisible"] = false
		s.activeIEMap["left"] = 0
		s.activeIEMap["top"] = 0
		s.activeIEMap["right"] = 0
		s.activeIEMap["bottom"] = 0
		s.activeIEMap["width"] = 0
		s.activeIEMap["height"] = 0
		s.aieTop["value"] = 0
		s.aieTop["y"] = 0
		s.aieBottom["value"] = 0
		s.aieBottom["y"] = 0
		s.aieLeft["value"] = 0
		s.aieLeft["x"] = 0
		s.aieRight["value"] = 0
		s.aieRight["x"] = 0
	}

	// root
	s.root["lookRight"] = m.LookRight
	s.root["totalCount"] = totalCount
}
