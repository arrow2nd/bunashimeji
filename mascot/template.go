package mascot

import (
	"fmt"
	"image"
	"math/rand/v2"

	"github.com/dop251/goja"
)

// CharacterTemplate は同じキャラの複数個体間で共有する不変データ。
// Actions / Behaviors / Images はパース後イミュータブル想定なので、複数 Mascot から
// 同じ参照を共有して安全。Breed で分裂するたびに XML/PNG 再読込するのを避ける。
//
// rgbaCache は (元 image.Image, lookRight) → *image.RGBA の共有キャッシュ。
// 個体ごとに RGBA を保持していた旧実装では同キャラ N 体で同じ RGBA が N 個重複していた。
// テンプレート単位で持つことで pose 数 × 2 (反転) 枚に圧縮される。
// Tick callback (単一スレッド) からのみアクセスされる前提でロックなし。
//
// sharedVM はテンプレート内の全 Mascot で共有する goja.Runtime。Runtime 1 個あたり
// 数 MB のオーバーヘッドが個体数に比例していたのを、同キャラ N 体で 1 個に圧縮する。
// refreshVM で毎 tick `mascot` グローバルを上書きすることで個体コンテキストを切り替える。
// 同キャラの Action パラメータは同じセットなので、bindActionParamsToVM が Action 開始時に
// 必要な変数をすべて set し直し、個体間で前の値が漏れることはない。
// goja.Runtime は goroutine セーフではないが、Tick callback は単一スレッドなので安全。
type CharacterTemplate struct {
	Name            string
	Actions         map[string]*Action
	Behaviors       []*Behavior
	ConditionGroups []ConditionGroup
	Images          map[string]image.Image
	Constants       CharacterConstants

	rgbaCache map[rgbaCacheKey]*image.RGBA
	sharedVM  *goja.Runtime

	// imagesNormalized は Images のキーを normalizeImageKey で正規化したマップ。
	// pose 切替時に XML 側のキー表記 (大文字小文字・スラッシュ向きが揺れる) で引いて
	// ミスマッチした際の線形再検索を O(1) にするため、ロード時に 1 度だけ構築する。
	imagesNormalized map[string]image.Image
}

// LoadCharacterTemplate はキャラ名から CharacterTemplate を構築する。
// confRoot/imgRoot はそれぞれ "conf"/"img" 等のディレクトリを想定。
// 1 キャラにつき 1 回だけ呼び、結果は main.go 側でキャッシュして再利用する。
func LoadCharacterTemplate(confRoot, imgRoot, name string) (*CharacterTemplate, error) {
	actions, err := LoadActions(confRoot, name)
	if err != nil {
		return nil, fmt.Errorf("load actions: %w", err)
	}
	behaviors, groups, consts, err := LoadBehaviors(confRoot, name)
	if err != nil {
		return nil, fmt.Errorf("load behaviors: %w", err)
	}
	imgs, err := loadImages(imgRoot, name)
	if err != nil {
		return nil, fmt.Errorf("load images: %w", err)
	}
	normalized := make(map[string]image.Image, len(imgs))
	for k, v := range imgs {
		normalized[normalizeImageKey(k)] = v
	}
	return &CharacterTemplate{
		Name:             name,
		Actions:          actions,
		Behaviors:        behaviors,
		ConditionGroups:  groups,
		Images:           imgs,
		Constants:        consts,
		rgbaCache:        map[rgbaCacheKey]*image.RGBA{},
		sharedVM:         goja.New(),
		imagesNormalized: normalized,
	}, nil
}

// InstanceOpts は NewInstance のカスタマイズ。
// すべて zero 値ならテンプレートデフォルト (画面上空ランダム X、SitAndFaceMouse 開始)。
type InstanceOpts struct {
	// Anchor が image.Point{} (zero) ならテンプレートデフォルト位置を採用する。
	// それ以外なら指定座標で生成 (Breed の生成位置等)。
	Anchor image.Point

	// LookRight は初期向き。Anchor が zero (= デフォルト位置) のときも適用される。
	LookRight bool

	// InitialBehavior が "" ならデフォルト (SitAndFaceMouse → なければ最初の Behavior)。
	// Breed では BornBehavior を渡す。
	InitialBehavior string

	// Env を指定すると複数 Mascot で同じ Environment を共有する。
	// この場合 Tick から env.Refresh() を呼ばないので、呼び出し側が tick 開始前に
	// 1 回 Refresh する責任を持つ (= platform syscall を個体数倍ではなく 1 回に圧縮)。
	// nil なら従来通り個別 Environment を作成し、Tick 内で毎回 Refresh する。
	Env *Environment
}

// NewInstance はテンプレートから新しい Mascot インスタンスを構築する。
//   - registry: Broadcast/ScanMove ハンドシェイク用 (nil 可)
//   - spawner:  Breed 経由の子マスコット生成依頼受け口 (nil 可)
//   - totalCountFn: 同キャラの現在個体数を返す (nil 可、その場合 1 固定)
//   - opts: 位置・向き・初期 Behavior のオーバーライド
func (t *CharacterTemplate) NewInstance(
	registry *BroadcastRegistry,
	spawner Spawner,
	totalCountFn func() int,
	opts InstanceOpts,
) *Mascot {
	maxCount := t.Constants.MaxCount
	if maxCount <= 0 {
		maxCount = defaultMaxCount
	}

	// Env が指定されていれば全 Mascot で共有 (呼び出し側が tick ごとに Refresh する)。
	// 未指定なら個別 Environment を作成し、ここで初期 Refresh + Tick 内でも Refresh する。
	env := opts.Env
	sharedEnv := env != nil
	if !sharedEnv {
		env = &Environment{}
	}

	m := &Mascot{
		Name:            t.Name,
		Template:        t,
		Actions:         t.Actions,
		Behaviors:       t.Behaviors,
		ConditionGroups: t.ConditionGroups,
		Images:          t.Images,
		LookRight:       opts.LookRight,
		vm:              t.sharedVM,
		env:             env,
		sharedEnv:       sharedEnv,
		registry:        registry,
		spawner:         spawner,
		totalCountFn:    totalCountFn,
		maxCount:        maxCount,
	}
	m.markAirHandlers()
	if !sharedEnv {
		// 共有 env は呼び出し側が既に Refresh 済みである前提なので二重に呼ばない。
		m.env.Refresh()
	}

	// 位置決め: opts.Anchor 指定なら使う、未指定なら initialSpawnAnchor (全モニタの上空ランダム)。
	if opts.Anchor != (image.Point{}) {
		m.Anchor = opts.Anchor
	} else {
		m.Anchor = initialSpawnAnchor(m.env)
		m.LookRight = true // 上空 spawn は従来通り右向きデフォルト
	}

	// 初期 Behavior 決定:
	//   - opts.InitialBehavior 指定 → 名前で検索 (Breed の BornBehavior は XML の生名)
	//   - 未指定 → SitAndFaceMouse 役割 → なければ最初の Behavior
	var initial *Behavior
	if opts.InitialBehavior != "" {
		if b, ok := m.findBehaviorByName(opts.InitialBehavior); ok {
			initial = b
		}
	}
	if initial == nil {
		if b, ok := m.findBehaviorByRole("SitAndFaceMouse"); ok {
			initial = b
		} else if len(t.Behaviors) > 0 {
			initial = t.Behaviors[0]
		}
	}
	if initial != nil {
		m.startBehavior(initial)
	}
	return m
}

// initialSpawnAnchor は「真上に別モニタが無い」スクリーンを 1 枚ランダムに選び、
// その上空 (X ランダム / Y = Min.Y - 200) を返す。NewInstance の初期位置と、
// Mascot.respawnIfStranded のフォールバックリスポーンの両方で使う。
func initialSpawnAnchor(env *Environment) image.Point {
	candidates := topmostScreens(env.Screens)
	var scr image.Rectangle
	if len(candidates) > 0 {
		scr = candidates[rand.IntN(len(candidates))]
	} else {
		scr = env.CurrentScreen(image.Point{})
	}
	span := scr.Dx() - 200
	if span < 1 {
		span = 1
	}
	startX := scr.Min.X + 100 + rand.IntN(span)
	return image.Point{X: startX, Y: scr.Min.Y - 200}
}

// topmostScreens は「自分より Y が小さく X 範囲が重なる別スクリーンが存在しない」
// スクリーンだけを返す。縦積みマルチモニタの下側を初期 spawn 対象から外すために使う。
// 横並び (同一 Y) のスクリーン同士は互いに除外し合わないので、全枚数が候補になる。
func topmostScreens(screens []image.Rectangle) []image.Rectangle {
	var out []image.Rectangle
	for i, s := range screens {
		covered := false
		for j, other := range screens {
			if i == j {
				continue
			}
			if other.Min.Y < s.Min.Y && other.Max.X > s.Min.X && other.Min.X < s.Max.X {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, s)
		}
	}
	return out
}
