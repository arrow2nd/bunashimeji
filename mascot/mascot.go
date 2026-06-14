package mascot

import (
	"bytes"
	"fmt"
	"image"
	_ "image/png"
	"log"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"

	"github.com/dop251/goja"
)

// defaultMaxCount は <定数 maxCount> 未指定キャラに適用する個体数上限。
// 上限が無いと Breed が無制限に増殖するので、保守的な低めの値で抑える。
// 増やしたいキャラは behaviors.xml に <定数 Name="maxCount" 値="N" /> を書く。
const defaultMaxCount = 10

// DragState はマウスドラッグの状態。
type DragState int

const (
	DragNone     DragState = iota // ドラッグされていない
	DragStarted                   // 直前のフレームで掴まれた (Behavior 切替用)
	DragHolding                   // 掴まれて移動中
	DragReleased                  // 直前のフレームで離された (Throw 用)
)

type Mascot struct {
	Name      string
	Anchor    image.Point
	LookRight bool
	Velocity  image.Point

	// Template は生成元テンプレートへの参照 (NewInstance で設定)。
	// render 層から共有 RGBA キャッシュ (Template.RGBA) を引くのに使う。
	Template *CharacterTemplate

	Actions         map[string]*Action
	Behaviors       []*Behavior
	ConditionGroups []ConditionGroup

	CurrentBehavior *Behavior
	CurrentAction   *ActionState
	pendingNext     []BehaviorRef // NextBehavior 由来の次回候補
	pendingReplace  bool          // true なら通常抽選とマージせず pendingNext のみで決定

	Images map[string]image.Image // /shime1.png 等の正規化済みパス → 画像

	// 描画/ウィンドウ追従用 (render 層から参照)
	CurrentImage image.Image
	ImageAnchor  image.Point

	Drag       DragState
	dragOffset image.Point

	tick int
	vm   *goja.Runtime
	env  *Environment

	// sharedEnv は env が外部 (main loop 等) から渡された共有インスタンスかを示す。
	// true なら Tick 内で env.Refresh() を呼ばない (呼び出し側が tick 単位で 1 度
	// だけ Refresh する責任を持つ)。false なら従来通り Tick 冒頭で Refresh する。
	sharedEnv bool

	// jsScratch は refreshVM で goja に渡す mascot オブジェクト用の使い回し領域。
	// 初回呼び出しで遅延構築する。tick ごとに値だけ書き換えて map/closure の
	// 再アロケーションを避ける。
	jsScratch *jsScratch

	// registry は Broadcast / ScanMove のプロセス内ハンドシェイク用。
	// nil 可 (単独テスト等)。nil の場合 Broadcast/ScanMove は no-op。
	registry *BroadcastRegistry

	// forcedNext は ScanMove 到着時に外部から押し込まれる次 Behavior。
	// checkInterrupt で Drag より低・Fall より高の優先で消費される。
	forcedNext *BehaviorRef

	// activeIE 追従用の状態。前 tick 終了時に「乗っていた」場合の
	// ウィンドウ ID と矩形を保持し、次 tick 冒頭で移動量を anchor に反映する。
	onActiveWindow       bool
	prevActiveWindowID   uintptr
	prevActiveWindowRect image.Rectangle

	// 外部ウィンドウのグラブ駆動 (WalkWithIE / FallWithIE / ThrowIE) 用。
	// grabbedHWND が 0 でない間、毎 tick anchor+grabbedOffset の位置に
	// platform.MoveExternalWindow で外部ウィンドウを移動する。activeIE 追従
	// (followActiveWindowIfNeeded) は grab 中はバイパスする (二重移動回避)。
	grabbedHWND   uintptr
	grabbedOffset image.Point

	// Breed (分裂・召喚) サポート用。
	// spawner は新 Mascot 生成リクエストの受け口 (main.go が実装、Tick 末尾で drain)。
	// totalCountFn は同キャラの現在個体数を返す (mascot.totalCount JS 変数の真値ソース)。
	// maxCount は <定数 maxCount> の値 (mascot.maxCount JS 変数として公開)。
	spawner      Spawner
	totalCountFn func() int
	maxCount     int
}

// Tick は 1 フレーム分の状態更新を行う。
// runtime.md の処理順序: 環境更新 → ウィンドウ追従 → 割り込み → Action.Update → Behavior 遷移。
// 描画は render 層が CurrentImage / ImageAnchor を参照する。
func (m *Mascot) Tick() {
	m.tick++
	if !m.sharedEnv {
		// 個別 Environment の場合のみ syscall を叩く。共有時は呼び出し側が tick
		// 開始前に 1 回だけ Refresh する (= platform syscall を個体数倍ではなく
		// 1 回に圧縮するための最適化)。
		m.env.Refresh()
	}
	m.respawnIfStranded()
	m.followActiveWindowIfNeeded()
	m.refreshVM()
	defer m.updateOnActiveWindowState()

	if interrupt := m.checkInterrupt(); interrupt != nil {
		m.startBehavior(interrupt)
	}

	if m.CurrentAction == nil {
		m.advanceBehavior()
	}
	if m.CurrentAction != nil {
		done := StepAction(m.CurrentAction, m, m.env)
		m.refreshPose()
		if done {
			m.CurrentAction = nil
			// forcedNext (ScanMove 到着等で StepAction が即時遷移を要求した場合) を
			// advanceBehavior の通常抽選より優先して消費する。これをやらないと
			// 1 tick だけ無関係な Behavior が選ばれて即 checkInterrupt で
			// 上書きされる挙動になり、ポーズ切替がチラつく。
			if m.forcedNext != nil {
				next := m.forcedNext
				m.forcedNext = nil
				if b, ok := m.findBehaviorByName(next.Name); ok {
					m.startBehavior(b)
					return
				}
			}
			m.advanceBehavior()
		}
	}
}

// followActiveWindowIfNeeded は前 tick で activeIE 上に乗っていたなら、
// このウィンドウの移動量を anchor に反映してマスコットを追従させる。
//
// 適用条件:
//   - 前 tick 終了時に onActiveWindow=true (= 確実に乗っていた)
//   - Drag 中ではない (掴まれているなら追従しない)
//   - 現 tick の activeIE が visible かつ ID が前 tick と同じ (= 同一ウィンドウ)
//
// 乗っていたウィンドウが非表示になった場合 (最小化・クローズ・フォーカス喪失) は、
// **同 tick 内で** 直接 Fall Behavior に切り替えて即時落下を開始する。
// 次 tick の checkInterrupt 任せだと 40ms 遅れて見えるため。
//
// 別の (ホワイトリスト合致した) ウィンドウにフォーカスが移った場合は、元ウィンドウが
// まだ画面上に残っている可能性があるので即時 Fall は強制せず、checkInterrupt の
// 通常空中判定に任せる (1 tick 遅れで Fall 発動)。
func (m *Mascot) followActiveWindowIfNeeded() {
	if m.drivesGrabbedWindow() {
		// WalkWithIE / FallWithIE / ThrowIE 駆動中は anchor が動くと
		// MoveExternalWindow でウィンドウも移動する。activeIE 追従ロジックで
		// 同じ移動量をさらに anchor へ加算すると二重に動いてしまう。
		return
	}
	if !m.onActiveWindow {
		return
	}
	if m.Drag != DragNone {
		return
	}
	aw := m.env.ActiveWindow

	// 非表示化 → 即時 Fall
	if !aw.Visible {
		m.onActiveWindow = false
		m.forceFallNow()
		return
	}
	// 別ウィンドウへフォーカス移動 → 追従しない (Fall は次 tick の checkInterrupt 経由)
	if aw.ID != m.prevActiveWindowID {
		return
	}
	// 同一ウィンドウの移動 → 差分を anchor に反映
	dx := aw.Rect.Min.X - m.prevActiveWindowRect.Min.X
	dy := aw.Rect.Min.Y - m.prevActiveWindowRect.Min.Y
	if dx == 0 && dy == 0 {
		return
	}
	m.Anchor.X += dx
	m.Anchor.Y += dy
}

// respawnIfStranded は anchor がモニタ近傍の有効範囲外 (ありえない座標) になった場合、
// 初期 spawn と同じ位置 (topmost モニタ上空ランダム) へ teleport して Fall を強制する。
// 想定ユースケース: モニタ切断、Thrown が画面外で停止、JS 式の不具合等で anchor が
// 全モニタ範囲外に飛んでしまった際の救済。
// Drag 中はユーザが意図して画面外まで掴んでいる可能性があるのでスキップする。
func (m *Mascot) respawnIfStranded() {
	if m.Drag != DragNone {
		return
	}
	if m.env.IsAtValidPosition(m.Anchor) {
		return
	}
	m.Anchor = initialSpawnAnchor(m.env)
	m.LookRight = true
	if b, ok := m.findBehaviorByRole("Fall"); ok {
		m.startBehavior(b)
	}
}

// forceFallNow は CurrentBehavior を Fall に強制切替する。既に Fall 中なら no-op。
// 乗っていたウィンドウが非表示になった瞬間に「同 tick 内で落下開始」を実現するために使う。
// HandlesAir の判定はスキップする (元 Behavior が空中遷移を内包していても、
// 物理的に支えがなくなった以上は Fall を優先する)。
func (m *Mascot) forceFallNow() {
	if behaviorMatchesRole(m.CurrentBehavior, "Fall") {
		return
	}
	if b, ok := m.findBehaviorByRole("Fall"); ok {
		m.startBehavior(b)
	}
}

// drivesGrabbedWindow は WalkWithIE / FallWithIE / ThrowIE で外部ウィンドウを
// 駆動している最中かを返す。activeIE 追従ロジックは grab 中バイパスする。
func (m *Mascot) drivesGrabbedWindow() bool {
	return m.grabbedHWND != 0
}

// setGrab はグラブ開始時に呼ぶ。hwnd=0 を渡してはいけない (clearGrab を使うこと)。
// offset は (window.Rect.Min - anchor) で、毎 tick anchor+offset の位置へ
// ウィンドウを移動するのに使う (= マスコットと相対位置を維持)。
func (m *Mascot) setGrab(hwnd uintptr, offset image.Point) {
	m.grabbedHWND = hwnd
	m.grabbedOffset = offset
}

// clearGrab はグラブ解放。Action 完了・ドラッグ割り込み・ウィンドウ消失で呼ぶ。
func (m *Mascot) clearGrab() {
	m.grabbedHWND = 0
	m.grabbedOffset = image.Point{}
}

// GrabbedOffset は (window.Rect.Min - anchor) の差分を返す。
func (m *Mascot) GrabbedOffset() image.Point { return m.grabbedOffset }

// updateOnActiveWindowState は tick 末尾で「現在 activeIE のいずれかの辺にくっついているか」を
// 再評価し、次 tick の追従判定で使うウィンドウ ID と矩形を保存する。
// 上面 (Walk/Stand) だけでなく、JumpOnIELeftWall などで側面/底面に GrabWall した
// ケースも追従対象に含めるため IsAttachedToActiveWindow を使う。
func (m *Mascot) updateOnActiveWindowState() {
	aw := m.env.ActiveWindow
	if aw.Visible && m.env.IsAttachedToActiveWindow(m.Anchor) {
		m.onActiveWindow = true
		m.prevActiveWindowID = aw.ID
		m.prevActiveWindowRect = aw.Rect
	} else {
		m.onActiveWindow = false
	}
}

// Cleanup は Mascot 強制破棄時のクリーンアップ。
// 通常 Action の完了パスを通らずに死ぬケース (tray「1体にする」/ ctx メニュー「他を消す」/
// Transform) で、registry.entries に *Mascot 参照が残るのを防ぐために呼ぶ。
//
// 同名の Action 完了処理で既に Unregister 済みであれば二重呼び出しは無害 (該当 entry なし)。
// CurrentAction / forcedNext / jsScratch も明示的に切って大物参照を即座に手放す
// (どうせ呼び出し側で Mascot 自体が unreachable になるので意味は薄いが、
// 万一スタックフレームや defer 経由で Mascot が一時的に生き残ってもメモリは早く解放される)。
func (m *Mascot) Cleanup() {
	if m == nil {
		return
	}
	if m.registry != nil {
		m.registry.UnregisterMascot(m)
	}
	m.CurrentAction = nil
	m.CurrentBehavior = nil
	m.forcedNext = nil
	m.pendingNext = nil
	m.jsScratch = nil
}

// SetDragging は render 層からマウス入力を反映する。
// holding=true で開始、false で解放。
func (m *Mascot) SetDragging(holding bool) {
	switch m.Drag {
	case DragNone:
		if holding {
			m.Drag = DragStarted
			m.dragOffset = image.Point{
				X: m.Anchor.X - m.env.Cursor.X,
				Y: m.Anchor.Y - m.env.Cursor.Y,
			}
		}
	case DragStarted, DragHolding:
		if !holding {
			m.Drag = DragReleased
		}
	case DragReleased:
		if holding {
			m.Drag = DragStarted
		}
	}
}

// DragOffset はドラッグ開始時のカーソル→アンカー差分を返す (Dragged Action から使用)。
func (m *Mascot) DragOffset() image.Point { return m.dragOffset }

// Env は内部の Environment への参照を返す (read-only 用途)。
func (m *Mascot) Env() *Environment { return m.env }

// refreshVM は goja に最新の mascot オブジェクトをセットする。
// jsScratch を初回構築し、以後は値だけ書き換えて再利用することで
// 毎 tick の map / closure アロケーションを排除する。
func (m *Mascot) refreshVM() {
	count := 1
	if m.totalCountFn != nil {
		// 同キャラの現在個体数。Breed 系 Behavior の Condition
		// `mascot.totalCount < maxCount` のゲートに使う。
		count = m.totalCountFn()
	}
	if m.jsScratch == nil {
		m.jsScratch = newJSScratch()
	}
	m.jsScratch.update(m, m.env, count)
	m.vm.Set("mascot", m.jsScratch.root)
	// 本家のグローバル: maxCount はそのキャラの同時起動上限値。
	// behaviors.xml の <定数 Name="maxCount" 値="N" /> から取得。
	// 未指定なら defaultMaxCount。
	m.vm.Set("maxCount", m.maxCount)
	// FootX / FootY は本家がドラッグ時の Pinched Animation 等で参照する
	// マスコット足元位置のトップレベル変数 (= mascot.anchor)。
	// 旧日本語版は小文字始まりの footX/footY を使うので両方公開する。
	m.vm.Set("FootX", m.Anchor.X)
	m.vm.Set("FootY", m.Anchor.Y)
	m.vm.Set("footX", m.Anchor.X)
	m.vm.Set("footY", m.Anchor.Y)
}

// refreshPose は CurrentAction の現 Pose を CurrentImage に反映する。
func (m *Mascot) refreshPose() {
	if m.CurrentAction == nil {
		return
	}
	pose, ok := currentPoseOf(m.CurrentAction, m, m.env)
	if !ok {
		return
	}
	if pose.Image != "" {
		if img, ok := m.lookupImage(pose.Image); ok {
			m.CurrentImage = img
			m.ImageAnchor = pose.ImageAnchor
		}
	}
}

func (m *Mascot) lookupImage(path string) (image.Image, bool) {
	// XML の Image 値はキャラルートからの相対 (先頭 /)。
	// LookRight=false の場合 Java 版は左右反転画像を別ファイル名で持つことがあるが、
	// v1 では Velocity の符号で進行方向を決め、render 層で反転表示する。
	if img, ok := m.Images[path]; ok {
		return img, true
	}
	// 正規化 (大文字小文字、スラッシュ統一) マップで O(1) 再検索。
	// 旧実装は全画像を線形ループしながら毎要素で文字列正規化していたため、
	// pose 切替時のミスマッチパスで O(N) + N 回の文字列アロケが発生していた。
	if m.Template != nil {
		if img, ok := m.Template.imagesNormalized[normalizeImageKey(path)]; ok {
			return img, true
		}
	}
	return nil, false
}

// normalizeImageKey は画像キーを比較用に正規化する (大文字小文字・スラッシュ統一)。
// CharacterTemplate のロード時と lookupImage のフォールバック時で同じ規則を使う。
func normalizeImageKey(path string) string {
	return strings.ToLower(strings.ReplaceAll(path, "\\", "/"))
}

// startBehavior は新しい Behavior を起動し、最初の Action State をセットする。
func (m *Mascot) startBehavior(b *Behavior) {
	m.CurrentBehavior = b
	m.pendingNext = nil
	m.pendingReplace = false
	a, ok := m.Actions[b.Name]
	if !ok {
		log.Printf("warning: behavior %q has no matching action", b.Name)
		m.CurrentAction = nil
		return
	}
	m.CurrentAction = newActionState(a, m)
	m.refreshPose()
}

// advanceBehavior は現 Behavior 完了後の次状態を決める。
// pendingNext (NextBehavior 由来) があれば優先、なければ通常抽選。
func (m *Mascot) advanceBehavior() {
	if m.CurrentBehavior != nil && len(m.pendingNext) == 0 {
		m.pendingNext, m.pendingReplace = m.collectNextRefs(m.CurrentBehavior)
	}
	next := m.pickNextBehavior()
	if next == nil {
		// フォールバック
		if b, ok := m.findBehaviorByRole("SitAndFaceMouse"); ok {
			next = b
		}
	}
	if next != nil {
		m.startBehavior(next)
	}
}

// PlayBehaviorByRole は roleAliases の役割名から Behavior を引いて startBehavior する。
// 旧日本語版キャラ (例: "マウスの周りに集まる") も英語名 (例: "ChaseMouse") で発動できるよう、
// tray メニュー等の「役割で指定したい」呼び出し元から使う。該当 Behavior が無ければ false。
func (m *Mascot) PlayBehaviorByRole(role string) bool {
	b, ok := m.findBehaviorByRole(role)
	if !ok {
		return false
	}
	m.startBehavior(b)
	return true
}

// PlayActionByName は引数名の Action を強制再生する。コンテキストメニューからの手動再生用。
//
// 同名 Behavior が存在する場合は startBehavior 経由で正規ルートに乗せる
// (Behavior の終了・遷移ロジックが機能するため、再生後に通常挙動へ戻る)。
// 同名 Behavior が無い (= Sequence/Select の child や Embedded から参照される単発 Action 等)
// 場合は、CurrentBehavior は据え置きで CurrentAction だけ差し替える。
// Action 完了後の遷移は CurrentBehavior の advanceBehavior に従う。
//
// 未知の name に対しては false を返し、状態は変更しない。
func (m *Mascot) PlayActionByName(name string) bool {
	if b, ok := m.findBehaviorByName(name); ok {
		m.startBehavior(b)
		return true
	}
	a, ok := m.Actions[name]
	if !ok {
		return false
	}
	// メニュー単発再生では Behavior 遷移に乗らないので、Action 自身が終わらないと永久ループになる。
	// Type=Move で TargetX/Y も Duration も持たない Action (例: 「なーぬい置く」「転んで首が取れた」)
	// は自然終了の手段が無いため、Animation 1 周分の Duration を合成して必ず終わるようにする。
	if needsAutoMoveDuration(a) {
		if d := firstAnimTotalDuration(a); d > 0 {
			a = withSyntheticDuration(a, d)
		}
	}
	m.CurrentAction = newActionState(a, m)
	m.refreshPose()
	return true
}

// needsAutoMoveDuration は a が「Type=Move なのに自然終了の手段を持たない」かを返す。
// TargetX/Y 通過判定も Duration タイマーも効かない Move は menu 単発再生で永久ループする。
func needsAutoMoveDuration(a *Action) bool {
	if a == nil || a.Type != "Move" {
		return false
	}
	for _, k := range [...]string{"TargetX", "TargetY", "Duration"} {
		if _, ok := a.Params[k]; ok {
			return false
		}
	}
	return true
}

// firstAnimTotalDuration は a の最初の Animation のポーズ Duration 合計を返す。
// Animation Condition は評価せず宣言順最初のものを使う簡易見積もり。
// menu 単発再生の「1 周打ち切り」用なので厳密一致は不要。
func firstAnimTotalDuration(a *Action) int {
	if a == nil || len(a.Animations) == 0 {
		return 0
	}
	total := 0
	for _, p := range a.Animations[0].Poses {
		total += p.Duration
	}
	return total
}

// withSyntheticDuration は a のシャロークローンに Duration=d を注入して返す。
// 元の Action (m.Actions に格納された共有実体) を汚さないよう、Params マップだけ
// コピーしてから差し込む (resolveActionRefs の clone と同じパターン)。
func withSyntheticDuration(a *Action, d int) *Action {
	clone := *a
	clone.Params = make(map[string]*Evaluator, len(a.Params)+1)
	for k, v := range a.Params {
		clone.Params[k] = v
	}
	ev, _ := NewEvaluator(fmt.Sprintf("%d", d))
	clone.Params["Duration"] = ev
	return &clone
}

func (m *Mascot) findBehaviorByName(name string) (*Behavior, bool) {
	for _, b := range m.Behaviors {
		if b.Name == name {
			return b, true
		}
	}
	for _, g := range m.ConditionGroups {
		for _, b := range g.Behaviors {
			if b.Name == name {
				return b, true
			}
		}
	}
	return nil, false
}

// loadImages はキャラディレクトリ配下の PNG を読み込む。
// XML の Image 値 (例: "/shime1.png") をキーに格納する。
func loadImages(imgRoot, name string) (map[string]image.Image, error) {
	dir, err := caseInsensitiveDir(imgRoot, name)
	if err != nil {
		return nil, err
	}
	out := make(map[string]image.Image)
	err = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(p), ".png") {
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		img, _, err := image.Decode(bytes.NewReader(raw))
		if err != nil {
			return fmt.Errorf("decode %s: %w", p, err)
		}
		// キーは XML の Image 値と同じ "/相対パス" 形式
		rel, _ := filepath.Rel(dir, p)
		key := "/" + filepath.ToSlash(rel)
		out[key] = img
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// 補助: 抽選で重み付き選択
func weightedPick(weights []int) int {
	total := 0
	for _, w := range weights {
		if w > 0 {
			total += w
		}
	}
	if total <= 0 {
		return -1
	}
	r := rand.IntN(total)
	for i, w := range weights {
		if w <= 0 {
			continue
		}
		r -= w
		if r < 0 {
			return i
		}
	}
	return -1
}
