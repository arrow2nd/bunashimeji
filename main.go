package main

import (
	"context"
	"flag"
	"fmt"
	"image"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/arrow2nd/bunashimeji/mascot"
	"github.com/arrow2nd/bunashimeji/platform"
)

// 25 TPS = 40 ms / tick (本家 Java 版互換)。
// XML の Velocity / Gravity / Duration は 25 TPS 前提で書かれている。
const tickInterval = 40 * time.Millisecond

func main() {
	var (
		nameFlag        string
		confDir         string
		imgDir          string
		debugFlag       bool
		traceAffordance bool
	)
	flag.StringVar(&nameFlag, "name", "", "起動するキャラ名 (img/[名前]/)。空ならすべて起動")
	flag.StringVar(&confDir, "conf", "conf", "設定 XML のルートディレクトリ")
	flag.StringVar(&imgDir, "img", "img", "画像のルートディレクトリ")
	flag.BoolVar(&debugFlag, "debug", false, "Behavior/Action 遷移をすべてログ出力")
	flag.BoolVar(&traceAffordance, "trace-affordance", false, "Broadcast/ScanMove のアフォーダンスイベントだけログ出力 (-debug より優先)")
	flag.Parse()

	exe, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	exeDir := filepath.Dir(exe)
	if !filepath.IsAbs(confDir) {
		confDir = filepath.Join(exeDir, confDir)
	}
	if !filepath.IsAbs(imgDir) {
		imgDir = filepath.Join(exeDir, imgDir)
	}

	// 起動するキャラ一覧
	var names []string
	if nameFlag != "" {
		names = []string{nameFlag}
	} else {
		n, err := mascot.CharacterDirs(imgDir)
		if err != nil {
			log.Fatalf("character dirs: %v", err)
		}
		names = n
	}
	if len(names) == 0 {
		fmt.Fprintln(os.Stderr, "no characters found under", imgDir)
		return
	}

	// Win32 ウィンドウは作成スレッド (= main) でメッセージを処理する必要がある。
	runtime.LockOSThread()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// OS シグナル → cancel
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		s := <-sig
		log.Printf("received %v, shutting down...", s)
		cancel()
	}()

	// Broadcast / ScanMove はプロセス内のレジストリ経由でハンドシェイク。
	// 全 Mascot で共有する。1 プロセス N キャラなのでロック不要。
	registry := mascot.NewBroadcastRegistry()

	// キャラごとのテンプレート (XML/PNG ロード結果) をキャッシュ。
	// Breed で同キャラを増やすたびに再ロードしないように。
	templates := map[string]*mascot.CharacterTemplate{}
	for _, n := range names {
		tpl, err := mascot.LoadCharacterTemplate(confDir, imgDir, n)
		if err != nil {
			log.Printf("load template %q: %v", n, err)
			continue
		}
		templates[n] = tpl
		if debugFlag {
			// フォールバックで別キャラの XML を読んでいないかの診断用。
			// actions/behaviors/images の数を出せば、想定キャラとずれた時に気付ける。
			log.Printf("[%s] template loaded: actions=%d behaviors=%d images=%d",
				n, len(tpl.Actions), len(tpl.Behaviors), len(tpl.Images))
		}
	}
	if len(templates) == 0 {
		log.Fatal("no character templates loaded")
	}

	// 全 Mascot で共有する Environment。onTick 冒頭で 1 度だけ Refresh して
	// platform syscall (EnumDisplayMonitors / GetCursorPos / GetForegroundWindow 周り)
	// を個体数 N に対して N→1 倍に圧縮する。Refresh は NewInstance 内の位置決定でも
	// 使うので、ここで先に 1 回呼んでおく。
	env := &mascot.Environment{}
	env.Refresh()

	// Spawner: stepBreed からのリクエストを受けて、Tick callback 末尾で実 Mascot/Window を作る。
	sp := newSpawner(templates, registry, env, debugFlag, traceAffordance, cancel)

	// 初期キャラの作成も Spawner 経由で統一する。
	// (countByName が初期生成分も含めて正しくカウントされるため)
	for _, n := range names {
		if _, ok := templates[n]; !ok {
			continue
		}
		c := sp.spawnCharacter(mascot.SpawnRequest{
			ParentName: n,
			// Anchor zero → テンプレートデフォルト (全モニタからランダムに 1 枚選んで上空ランダム X)
		})
		if c == nil {
			continue
		}
		log.Printf("spawned %q at (%d,%d)", n, c.M.Anchor.X, c.M.Anchor.Y)
	}
	if len(sp.chars) == 0 {
		log.Fatal("no characters loaded")
	}
	defer func() {
		for _, c := range sp.chars {
			c.Destroy()
		}
	}()

	log.Printf("running %d character(s) in single process; right-click any sprite to exit", len(sp.chars))

	err = platform.RunMessageLoop(ctx, tickInterval, func() {
		// 共有 Environment を tick あたり 1 度だけ Refresh。各 Mascot.Tick は
		// sharedEnv フラグで自前 Refresh をスキップする。
		env.Refresh()
		// 既存マスコットを進める。Tick 中に sp.Spawn() で新規リクエストが積まれることがある。
		// 既存スライス変更を避けるため、ここではイテレーションだけ。
		for _, c := range sp.chars {
			c.Tick()
		}
		// Tick 完了後に pending を消化して新 Character 作成 → chars に追加。
		sp.drain()
	})
	if err != nil && err != context.Canceled {
		log.Fatalf("message loop: %v", err)
	}
}

// ----------- Spawner: stepBreed → Tick 末尾の遅延 spawn -----------

// spawner は mascot.Spawner を実装。Tick 中に積まれた SpawnRequest を保持し、
// Tick callback 末尾で drain() を呼ばれて実 Character (Mascot+Window) に変換する。
//
// chars / countByName は drain で更新するので Tick callback と同スレッドからのみ触る。
//
// env は全 Mascot で共有する Environment。onTick 冒頭で 1 度だけ Refresh される。
// 個体ごとに platform.Screens / GetCursorPos / GetActiveWindow を毎 tick 叩く挙動を
// 1 tick あたり 1 回に圧縮する (= 個体数 N に対する syscall コストを N→1 倍に削減)。
type spawner struct {
	pending []mascot.SpawnRequest

	templates       map[string]*mascot.CharacterTemplate
	registry        *mascot.BroadcastRegistry
	env             *mascot.Environment
	debug           bool
	traceAffordance bool
	cancel          context.CancelFunc

	chars       []*Character
	countByName map[string]int
}

func newSpawner(
	templates map[string]*mascot.CharacterTemplate,
	registry *mascot.BroadcastRegistry,
	env *mascot.Environment,
	debug bool,
	traceAffordance bool,
	cancel context.CancelFunc,
) *spawner {
	return &spawner{
		templates:       templates,
		registry:        registry,
		env:             env,
		debug:           debug,
		traceAffordance: traceAffordance,
		cancel:          cancel,
		countByName:     map[string]int{},
	}
}

// Spawn は mascot.Spawner の実装。stepBreed から Tick 中に呼ばれる。
// 即時にウィンドウ作成すると tick 中の chars スライスを荒らすので、キューに積むだけ。
func (s *spawner) Spawn(req mascot.SpawnRequest) {
	s.pending = append(s.pending, req)
}

// drain は pending を消化して新 Character を chars に追加する。
// Tick callback の末尾 (= range chars 完了後) で呼ぶ前提。
// Win32 ウィンドウ生成はメインスレッド必須なので、ここで実行する。
func (s *spawner) drain() {
	if len(s.pending) == 0 {
		return
	}
	for _, req := range s.pending {
		c := s.spawnCharacter(req)
		if c != nil {
			log.Printf("[%s] spawned (total=%d)", req.ParentName, s.countByName[req.ParentName])
			_ = c
		}
	}
	s.pending = s.pending[:0]
}

// spawnCharacter は SpawnRequest 1 件を実 Character に変換して chars / countByName に追加する。
// 失敗時 (テンプレート無し / ウィンドウ生成失敗) は nil を返してログ出力。
//
// 初期キャラ生成 (main.go 起動時) もこのパスを通すので、countByName が
// 初期分も含めて整合する。
func (s *spawner) spawnCharacter(req mascot.SpawnRequest) *Character {
	tpl, ok := s.templates[req.ParentName]
	if !ok {
		log.Printf("spawn: no template for %q", req.ParentName)
		return nil
	}
	// totalCountFn は同キャラの現在個体数を返す。Behavior の
	// Condition `mascot.totalCount < maxCount` のゲートに使われる。
	// クロージャで s.countByName[name] を毎回参照する。
	name := req.ParentName
	counter := func() int { return s.countByName[name] }

	inst := tpl.NewInstance(s.registry, s, counter, mascot.InstanceOpts{
		Anchor:          req.Anchor,
		LookRight:       req.LookRight,
		InitialBehavior: req.InitialBehavior,
		Env:             s.env,
	})
	// traceAffordance が立っていればアフォーダンスログだけに絞る (debug より優先)。
	// debug 単独なら従来通り全イベントを流す。
	switch {
	case s.traceAffordance:
		wireAffordanceCallback(inst)
	case s.debug:
		wireDebugCallbacks(inst)
	}

	c, err := newCharacterFromMascot(inst, s.cancel)
	if err != nil {
		log.Printf("spawn %q: window create failed: %v", req.ParentName, err)
		return nil
	}
	s.chars = append(s.chars, c)
	s.countByName[req.ParentName]++
	return c
}

// wireDebugCallbacks は -debug 時にコンソールへ Behavior/Action 遷移を流すためのフック。
func wireDebugCallbacks(m *mascot.Mascot) {
	m.OnBehaviorChange = func(name string) {
		log.Printf("[%s] behavior: %s", m.Name, name)
	}
	m.OnActionEnter = func(actionName, atype string) {
		if actionName == "" {
			actionName = "<inline>"
		}
		log.Printf("[%s]   action: %s (%s)", m.Name, actionName, atype)
	}
	m.OnMoveStart = func(actionName string, target, anchor image.Point) {
		scr := m.Env().CurrentScreen(anchor)
		log.Printf("[%s]     move %q: target=(%d,%d) anchor=(%d,%d) screen=[%d..%d, %d..%d]",
			m.Name, actionName, target.X, target.Y, anchor.X, anchor.Y,
			scr.Min.X, scr.Max.X, scr.Min.Y, scr.Max.Y)
	}
	m.OnAffordance = func(event, affordance, detail string) {
		log.Printf("[%s]   AFFORDANCE %s: %q (%s)", m.Name, event, affordance, detail)
	}
}

// wireAffordanceCallback は OnAffordance だけを wire する (-trace-affordance 用)。
// Behavior/Action/Move のノイズを排してアフォーダンス成立を観察したい場面で使う。
func wireAffordanceCallback(m *mascot.Mascot) {
	m.OnAffordance = func(event, affordance, detail string) {
		log.Printf("[%s] AFFORDANCE %s: %q (%s)", m.Name, event, affordance, detail)
	}
}

// ----------- Character: Mascot + Win32 ウィンドウ -----------

// Character は 1 体の Mascot とそれを描画する Win32 ウィンドウを束ねる。
// 画像 RGBA キャッシュは Mascot.Template が共有保持するため、ここでは保持しない。
// 直前 tick の状態だけ持って差分検知に使う。
type Character struct {
	M *mascot.Mascot
	W *platform.Win32Window

	// 直前 tick で適用した状態 (差分検知用)
	lastValid bool
	lastImage image.Image
	lastFlip  bool
	lastPos   image.Point
}

// newCharacterFromMascot は構築済み Mascot を受け取って Win32 ウィンドウを作成し、
// Character に束ねる。Spawner.spawnCharacter から呼ばれる (初期キャラ・Breed 子マスコット共通)。
func newCharacterFromMascot(m *mascot.Mascot, cancel context.CancelFunc) (*Character, error) {
	// SetWindowRgn により不透明部のみ WM_LBUTTONDOWN が届くため、
	// 追加のピクセル alpha 判定は不要。
	handlers := platform.WindowHandlers{
		OnLeftDown: func(_, _ int) {
			m.SetDragging(true)
		},
		OnLeftUp: func(_, _ int) {
			m.SetDragging(false)
		},
		OnRightDown: func(_, _ int) {
			log.Printf("[%s] right-click → exit", m.Name)
			cancel()
		},
	}

	// ウィンドウは仮サイズで作る。最初の SetBitmap で実サイズに更新される。
	// Layered=true なので Show() を呼ぶまで非表示。
	w, err := platform.NewWin32Window(platform.WindowOpts{
		Title:    "bunashimeji - " + m.Name,
		X:        m.Anchor.X,
		Y:        m.Anchor.Y,
		Width:    1,
		Height:   1,
		Layered:  true,
		Handlers: handlers,
	})
	if err != nil {
		return nil, err
	}

	return &Character{
		M: m,
		W: w,
	}, nil
}

// Tick は 1 フレーム分: Mascot 状態を進めてからウィンドウへ反映する。
func (c *Character) Tick() {
	c.M.Tick()

	src := c.M.CurrentImage
	if src == nil {
		return
	}
	// RGBA はテンプレートで共有キャッシュされている。同キャラ複数体でも
	// 同じ (src, lookRight) なら同一バッファを共有する。
	rgba := c.M.Template.RGBA(src, c.M.LookRight)

	// ウィンドウ位置 = Anchor − ImageAnchor。
	// Win32 SetWindowPos / UpdateLayeredWindow は仮想デスクトップ絶対座標を直接受け取れるので、
	// ebitengine 版で必要だったモニタ原点減算は不要。
	pos := image.Point{
		X: c.M.Anchor.X - c.M.ImageAnchor.X,
		Y: c.M.Anchor.Y - c.M.ImageAnchor.Y,
	}

	if c.lastValid && c.lastImage == src && c.lastFlip == c.M.LookRight {
		// pose 同一 → 位置だけ更新
		if pos == c.lastPos {
			return
		}
		if err := c.W.SetBitmap(rgba, pos); err != nil {
			log.Printf("[%s] SetBitmap (move): %v", c.M.Name, err)
			return
		}
		c.lastPos = pos
		return
	}

	// pose 変化: bitmap + click mask + 位置を更新
	if err := c.W.SetBitmap(rgba, pos); err != nil {
		log.Printf("[%s] SetBitmap (pose): %v", c.M.Name, err)
		return
	}
	if err := c.W.SetClickMask(rgba); err != nil {
		log.Printf("[%s] SetClickMask: %v", c.M.Name, err)
	}
	if !c.lastValid {
		c.W.Show()
	}
	c.lastImage = src
	c.lastFlip = c.M.LookRight
	c.lastValid = true
	c.lastPos = pos
}

// Destroy はウィンドウを破棄する。
func (c *Character) Destroy() {
	if c.W != nil {
		c.W.Destroy()
		c.W = nil
	}
}
