package main

import (
	"context"
	"flag"
	"fmt"
	"image"
	"log"
	"math/rand/v2"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"sync"
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
		windowsConfig   string
		debugFlag       bool
		traceAffordance bool
	)
	flag.StringVar(&nameFlag, "name", "", "起動するキャラ名 (img/[名前]/)。空ならすべて起動")
	flag.StringVar(&confDir, "conf", "conf", "設定 XML のルートディレクトリ")
	flag.StringVar(&imgDir, "img", "img", "画像のルートディレクトリ")
	flag.StringVar(&windowsConfig, "windows-config", "", "ウィンドウ追従ホワイトリストの JSON (省略時は [conf]/windows.json)")
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
	if windowsConfig == "" {
		windowsConfig = filepath.Join(confDir, "windows.json")
	} else if !filepath.IsAbs(windowsConfig) {
		windowsConfig = filepath.Join(exeDir, windowsConfig)
	}

	// ウィンドウ追従ホワイトリストの設定をロード。ファイル不在は (空 cfg, nil) なので、
	// その場合は全プリセット有効・ユーザ追加無しで起動する。パースエラーは警告ログのみで
	// 起動を止めず、デフォルト相当で進む。
	winCfg, err := platform.LoadWhitelistConfig(windowsConfig)
	if err != nil {
		log.Printf("windows-config: %v (using defaults)", err)
	}
	platform.InstallWhitelistConfig(windowsConfig, winCfg)

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
	loadTemplate := func(n string) {
		if _, ok := templates[n]; ok {
			return
		}
		tpl, err := mascot.LoadCharacterTemplate(confDir, imgDir, n)
		if err != nil {
			log.Printf("load template %q: %v", n, err)
			return
		}
		templates[n] = tpl
		if debugFlag {
			// フォールバックで別キャラの XML を読んでいないかの診断用。
			// actions/behaviors/images の数を出せば、想定キャラとずれた時に気付ける。
			log.Printf("[%s] template loaded: actions=%d behaviors=%d images=%d",
				n, len(tpl.Actions), len(tpl.Behaviors), len(tpl.Images))
		}
	}
	for _, n := range names {
		loadTemplate(n)
	}
	// Transform 先キャラのテンプレートも自動ロード。
	// -name Hayate 単独起動でも、Hayate の Action に TransformMascot="Nagi" があれば
	// Nagi の template/image を読み込んでおかないと変身要求が捨てられる。
	// 直接ロードしたキャラの Actions だけスキャンする (推移閉包までは追わない)。
	for _, tpl := range templates {
		for _, a := range tpl.Actions {
			if a.TransformMascot != "" {
				loadTemplate(a.TransformMascot)
			}
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

	// タスクバー常駐 (Win32) を起動。tray メニュー操作は別 goroutine で発生するため、
	// chars / Mascot 状態を触る系 (ふやす / あつまれ / 1匹だけのこす) は queueMutation 経由で
	// main thread に橋渡しし tick 冒頭で消化する。
	// 「ばいばい」は context.CancelFunc が thread-safe なので直接呼ぶ。
	startTray(TrayCallbacks{
		OnSpawnRandom: func() {
			sp.queueMutation(func(s *spawner) { s.spawnRandom() })
		},
		OnGather: func() {
			sp.queueMutation(func(s *spawner) { s.gatherAll() })
		},
		OnKeepOne: func() {
			sp.queueMutation(func(s *spawner) { s.keepOnly(nil) })
		},
		OnQuit: func() { cancel() },
	})
	defer stopTray()

	log.Printf("running %d character(s) in single process; right-click a sprite for menu, tray menu to exit", len(sp.chars))

	err = platform.RunMessageLoop(ctx, tickInterval, func() {
		// 共有 Environment を tick あたり 1 度だけ Refresh。各 Mascot.Tick は
		// sharedEnv フラグで自前 Refresh をスキップする。
		env.Refresh()
		// tray / ctx menu からのリクエストを最初に消化。Tick 内で破棄済み Character の
		// c.W=nil を参照しないよう、必ず iteration の前に行う。
		sp.drainMutations()
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
	pending          []mascot.SpawnRequest
	pendingTransform []mascot.TransformRequest

	templates       map[string]*mascot.CharacterTemplate
	registry        *mascot.BroadcastRegistry
	env             *mascot.Environment
	debug           bool
	traceAffordance bool
	cancel          context.CancelFunc

	chars       []*Character
	countByName map[string]int

	// mutations は tray (別 goroutine) およびコンテキストメニュー (wndproc = main thread) から
	// 積まれる「chars / countByName を変更する関数」のキュー。tick 冒頭で drain して
	// main thread で順次実行する。Win32 DestroyWindow を作成スレッド以外から呼ぶと
	// 失敗するため、変更を必ず main thread に集約する目的。
	// tray からの push と tick 内 drain の競合があるため mutex で保護する。
	mutMu     sync.Mutex
	mutations []func(*spawner)
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

// Transform は mascot.Spawner の実装。stepTransform から Tick 中に呼ばれる。
// 即時に旧 Character.Destroy + 新 spawn をすると Tick 中の chars スライスを荒らすので、
// キューに積むだけ。実差し替えは drain 末尾の transformCharacter で行う。
func (s *spawner) Transform(req mascot.TransformRequest) {
	s.pendingTransform = append(s.pendingTransform, req)
}

// queueMutation は chars / countByName への変更を mutations キューに積む。
// tray (別 goroutine) や ctx メニュー (wndproc = main) のどちらからでも安全に呼べる。
// 実体は drainMutations で main thread から実行される。
func (s *spawner) queueMutation(fn func(*spawner)) {
	s.mutMu.Lock()
	s.mutations = append(s.mutations, fn)
	s.mutMu.Unlock()
}

// drainMutations は積まれた変更を順に適用する。tick 冒頭 (Tick ループより前) で呼ぶ前提。
// Tick より後に呼ぶと、破棄済み Character の c.W=nil を Tick 内で参照して panic する。
func (s *spawner) drainMutations() {
	s.mutMu.Lock()
	q := s.mutations
	s.mutations = nil
	s.mutMu.Unlock()
	for _, fn := range q {
		fn(s)
	}
}

// removeOne は target 1 体だけ破棄して chars から取り除く。target が chars に居なければ no-op。
// countByName は対応キャラのカウンタを 1 減らす (Behavior の maxCount ゲートが再び効くようになる)。
func (s *spawner) removeOne(target *Character) {
	if target == nil {
		return
	}
	for i, c := range s.chars {
		if c != target {
			continue
		}
		c.Destroy()
		s.chars = append(s.chars[:i], s.chars[i+1:]...)
		if n := s.countByName[c.M.Name]; n > 0 {
			s.countByName[c.M.Name] = n - 1
		}
		log.Printf("removed %q (remaining=%d)", c.M.Name, len(s.chars))
		// 全滅したらアプリ終了 (タスクバー右クリックの「終了」と挙動を合わせる)。
		if len(s.chars) == 0 {
			s.cancel()
		}
		return
	}
}

// spawnRandom は s.templates から名前を 1 つランダムに選んで spawn する。
// tray「ふやす」用。テンプレートが空 (= keepOnly 後に dropUnusedTemplates で減って 1 個だけ
// しか残っていない状況も含む) でも残ったものから 1 つ選ぶ。0 個ならログを残して no-op。
//
// 配置はテンプレートデフォルト (上空ランダム X)。maxCount は通常抽選 (Behavior Condition)
// のゲートで効くもので、手動 spawn にはかけない (= ユーザ操作は常に通る方が直感的)。
func (s *spawner) spawnRandom() {
	if len(s.templates) == 0 {
		log.Printf("spawnRandom: no templates loaded")
		return
	}
	names := make([]string, 0, len(s.templates))
	for n := range s.templates {
		names = append(names, n)
	}
	sort.Strings(names) // map の iteration order を安定させてからランダム抽選
	name := names[rand.IntN(len(names))]
	if c := s.spawnCharacter(mascot.SpawnRequest{ParentName: name}); c != nil {
		log.Printf("spawned %q via tray ふやす (total=%d)", name, len(s.chars))
	}
}

// gatherAll は全キャラの現 Behavior を ChaseMouse に強制切替する。tray「あつまれ！」用。
// ChaseMouse 自体は本家の「カーソル方向へ歩いて到達したら座る」シーケンスなので、
// 到達後の SitAndFaceMouse → 離脱タイミングはキャラ任せ (= XML 定義に従う)。
// 空中・Drag 中のキャラは次 tick の checkInterrupt で Fall/Dragged に再度奪われるが、
// 「完全にキャラ任せ」のスペックなのでそれで OK (落ちてから自由行動に戻る)。
func (s *spawner) gatherAll() {
	for _, c := range s.chars {
		if c.M == nil {
			continue
		}
		if !c.M.PlayBehaviorByRole("ChaseMouse") {
			log.Printf("[%s] gather: ChaseMouse not found", c.M.Name)
		}
	}
}

// keepOnly は keeper 1 体だけ残して他をすべて破棄する。
// keeper=nil は chars[0] を keeper と見なす (tray の「しめじを1つにする」用)。
// keeper が chars に居ない場合は何もしない。
func (s *spawner) keepOnly(keeper *Character) {
	if len(s.chars) <= 1 {
		return
	}
	if keeper == nil {
		keeper = s.chars[0]
	}
	survivors := make([]*Character, 0, 1)
	for _, c := range s.chars {
		if c == keeper {
			survivors = append(survivors, c)
		} else {
			c.Destroy()
		}
	}
	if len(survivors) == 0 {
		// keeper が chars に含まれていなかった: 何もしないで戻す
		return
	}
	s.chars = survivors
	for k := range s.countByName {
		s.countByName[k] = 0
	}
	s.countByName[keeper.M.Name]++
	log.Printf("kept only %q", keeper.M.Name)
	// 生き残りキャラ + その Transform 先テンプレート以外を破棄する。
	// PNG decoded `image.Image` と rgbaCache の RGBA バッファが解放対象で、
	// 17 キャラぶんに数十 MB 〜 100 MB 級の効果が出る。
	s.dropUnusedTemplates()
	// 大量破棄直後は Go heap に未使用領域が大量に残るので、OS に積極的に返却する。
	// GC を強制 → FreeOSMemory で MEM_DECOMMIT 相当を発行することで、タスクマネージャの
	// 使用量を実際に縮める。tray メニュー操作は頻度が低いので毎回叩いて問題ない。
	runtime.GC()
	debug.FreeOSMemory()
}

// dropUnusedTemplates は s.chars に居ないキャラのテンプレートを s.templates から削除する。
// 生き残りキャラの Action TransformMascot で参照されるテンプレートは保持する
// (Transform 推移閉包は main.go 起動時と同じく 1 段のみ追う)。
//
// テンプレートを丸ごと落とせば Images map / rgbaCache / sharedVM (goja.Runtime) が
// すべて unreachable になり、後続の runtime.GC + debug.FreeOSMemory で OS に返る。
func (s *spawner) dropUnusedTemplates() {
	alive := make(map[string]struct{}, len(s.chars))
	for _, c := range s.chars {
		if c.M != nil {
			alive[c.M.Name] = struct{}{}
		}
	}
	// 1 段ぶん Transform 先を追加 (推移閉包は追わない = 起動時挙動と同じ)。
	// イテレーション中に alive に追記しないよう、スナップショットを先に取る。
	directlyAlive := make([]string, 0, len(alive))
	for name := range alive {
		directlyAlive = append(directlyAlive, name)
	}
	for _, name := range directlyAlive {
		tpl, ok := s.templates[name]
		if !ok {
			continue
		}
		for _, a := range tpl.Actions {
			if a.TransformMascot != "" {
				alive[a.TransformMascot] = struct{}{}
			}
		}
	}
	for name := range s.templates {
		if _, ok := alive[name]; ok {
			continue
		}
		delete(s.templates, name)
		// countByName は keepOnly が既に 0 リセット済みだが、念のためエントリも消す。
		delete(s.countByName, name)
		log.Printf("dropped template %q (no live instances)", name)
	}
}

// drain は pending を消化して新 Character を chars に追加する。
// Tick callback の末尾 (= range chars 完了後) で呼ぶ前提。
// Win32 ウィンドウ生成はメインスレッド必須なので、ここで実行する。
//
// 順序は Spawn → Transform。Transform は変身先キャラの新 spawn 込みなので、
// 後段に置いておく方が「最後に残るキャラ数」のログが直感的になる。
func (s *spawner) drain() {
	for _, req := range s.pending {
		c := s.spawnCharacter(req)
		if c != nil {
			log.Printf("[%s] spawned (total=%d)", req.ParentName, s.countByName[req.ParentName])
			_ = c
		}
	}
	s.pending = s.pending[:0]

	for _, req := range s.pendingTransform {
		s.transformCharacter(req)
	}
	s.pendingTransform = s.pendingTransform[:0]
}

// transformCharacter は TransformRequest 1 件を処理する:
//  1. NewName のテンプレートが無ければ警告して諦める (旧キャラ存続)
//  2. 旧 Character を chars から探す (見つからなければ無視)
//  3. **新 Character を先に spawn** → 4. 旧 Character を Destroy + chars から除去
//
// 順序が「新 spawn → 旧 destroy」なのは、削除 → chars 空になった瞬間に removeOne 経路の
// 自動 cancel() が走るのを避けるため (Transform は減算ではないので終了させたくない)。
func (s *spawner) transformCharacter(req mascot.TransformRequest) {
	if _, ok := s.templates[req.NewName]; !ok {
		log.Printf("transform: no template for %q (skipped, source kept)", req.NewName)
		return
	}

	var (
		old    *Character
		oldIdx = -1
	)
	for i, c := range s.chars {
		if c.M == req.Self {
			old = c
			oldIdx = i
			break
		}
	}
	if old == nil {
		log.Printf("transform: source mascot already gone (race)")
		return
	}

	anchor := old.M.Anchor
	lookRight := old.M.LookRight
	oldName := old.M.Name

	// 先に新キャラを spawn (失敗時は旧キャラを残す)。
	newChar := s.spawnCharacter(mascot.SpawnRequest{
		ParentName:      req.NewName,
		Anchor:          anchor,
		LookRight:       lookRight,
		InitialBehavior: req.InitialBehavior,
	})
	if newChar == nil {
		// spawnCharacter 内でログ済み
		return
	}

	// 旧キャラを破棄して chars から除去。countByName も整える。
	old.Destroy()
	s.chars = append(s.chars[:oldIdx], s.chars[oldIdx+1:]...)
	if n := s.countByName[oldName]; n > 0 {
		s.countByName[oldName] = n - 1
	}

	log.Printf("[%s] transformed → %s @ (%d,%d)", oldName, req.NewName, anchor.X, anchor.Y)
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

	c, err := s.newCharacterFromMascot(inst)
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
//
// 右クリックハンドラはコンテキストメニューを表示するため self (*Character) と spawner を
// クロージャで握る必要がある。self はウィンドウ生成後にしかセットできないため、変数を先に
// 宣言してハンドラから参照し、return 直前に代入する (wndproc は Show() 後の最初の input まで
// 発火しないので、c=nil で発火することはない)。
func (s *spawner) newCharacterFromMascot(m *mascot.Mascot) (*Character, error) {
	var self *Character
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
			if self == nil {
				return
			}
			s.showContextMenu(self)
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

	self = &Character{
		M: m,
		W: w,
	}
	return self, nil
}

// ----------- コンテキストメニュー -----------

// メニュー leaf の command ID。0 は TrackPopupMenu の「キャンセル」と衝突するので 1 以上。
// Action 列の ID は ctxCmdActionBase から連番で割り当て、選択時に nameByID で名前に逆引きする。
const (
	ctxCmdRemoveSelf = 1
	ctxCmdKeepSelf   = 2
	ctxCmdSpawnPeer  = 3
	ctxCmdActionBase = 100
)

// showContextMenu はキャラ右クリック時に呼ばれて、target に対するコンテキストメニューを開く。
// wndproc から (= main thread) 同期的に呼ばれるが、選択された結果は queueMutation 経由で
// 次の tick に遅延適用する。TrackPopupMenu 中も chars イテレーションは走らないため、
// 即時 chars を破壊しても直接 panic にはならないが、tick との順序を明示するためキュー経由に統一。
func (s *spawner) showContextMenu(target *Character) {
	if target == nil || target.W == nil {
		return
	}
	// Action 名一覧を安定順 (アルファベット順) に並べる。map の iteration order が
	// ランダムだとメニュー位置が毎回変わって押し間違える。
	actionNames := make([]string, 0, len(target.M.Actions))
	for name := range target.M.Actions {
		actionNames = append(actionNames, name)
	}
	sort.Strings(actionNames)

	nameByID := make(map[int]string, len(actionNames))
	subItems := make([]platform.MenuItem, 0, len(actionNames))
	for i, name := range actionNames {
		id := ctxCmdActionBase + i
		nameByID[id] = name
		subItems = append(subItems, platform.MenuItem{ID: id, Label: name})
	}

	items := []platform.MenuItem{
		{ID: ctxCmdKeepSelf, Label: "このしめじだけ残す"},
		{ID: ctxCmdSpawnPeer, Label: "もう1匹呼ぶ"},
		{ID: ctxCmdRemoveSelf, Label: "帰ってもらう"},
		{Separator: true},
		{
			Label:    "アクションを選んで再生",
			Submenu:  subItems,
			Disabled: len(subItems) == 0,
		},
	}

	cursor := platform.CursorPosition()
	cmd := platform.ShowPopupMenu(target.W.HWND(), cursor.X, cursor.Y, items)
	switch {
	case cmd == 0:
		// dismiss
	case cmd == ctxCmdRemoveSelf:
		s.queueMutation(func(sp *spawner) { sp.removeOne(target) })
	case cmd == ctxCmdKeepSelf:
		s.queueMutation(func(sp *spawner) { sp.keepOnly(target) })
	case cmd == ctxCmdSpawnPeer:
		// 同キャラの仲間を 1 体追加。target が破棄されても name が残るよう値で捕捉する。
		// 配置は tray「ふやす」と同じくテンプレートデフォルト (上空ランダム X)。
		name := target.M.Name
		s.queueMutation(func(sp *spawner) {
			if c := sp.spawnCharacter(mascot.SpawnRequest{ParentName: name}); c != nil {
				log.Printf("spawned %q via ctx もう1匹呼ぶ (total=%d)", name, len(sp.chars))
			}
		})
	default:
		name, ok := nameByID[cmd]
		if !ok {
			return
		}
		s.queueMutation(func(_ *spawner) {
			if !target.M.PlayActionByName(name) {
				log.Printf("[%s] play action %q: not found", target.M.Name, name)
			}
		})
	}
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
// Mascot 側にも Cleanup を伝播し、BroadcastRegistry に残った自分の entry を解除する。
// これをやらないと「Broadcast Action 再生中に強制破棄された Mascot」が registry.entries
// から参照され続け、jsScratch ごと GC されない。
//
// c.M は nil 化しない: removeOne 等の呼び出し側が Destroy 後にも c.M.Name を参照する。
// 重い参照 (jsScratch / CurrentAction 等) は Cleanup 内で個別 nil 化されるので、
// Mascot struct 自体の本体は小さく、c.M 経由で残っても問題にならない。
func (c *Character) Destroy() {
	if c.M != nil {
		c.M.Cleanup()
	}
	if c.W != nil {
		c.W.Destroy()
		c.W = nil
	}
}
