# 実行時仕様

データ構造、Behavior ステートマシン、Action 更新ロジック、tick 内処理順序、描画/ウィンドウ戦略、platform 層をまとめる。XML フォーマットそのものは [xml-format.md](xml-format.md) を参照。

## アーキテクチャ概要

**1 プロセス N キャラ N 透過ウィンドウ**。shimeji-ee Java 版と同等の構成。

- メインスレッド (`runtime.LockOSThread`) が Win32 メッセージポンプを所有
- ウィンドウは `WS_EX_LAYERED` 付き、`UpdateLayeredWindow` で per-pixel alpha 描画
- クリック領域は `SetWindowRgn` で alpha>0 の矩形配列に絞る → 透明部はクリックスルー
- 25 TPS (40ms/tick) で全 Mascot を順次 Tick → pose 変化のみ `UpdateLayeredWindow` 発火
- Broadcast / ScanMove は同プロセス内 `BroadcastRegistry` 経由 (IPC 不要)
- activeIE (アクティブウィンドウ) は `GetForegroundWindow` + ホワイトリストで判定して JS API として公開 (詳細: [window-whitelist.md](window-whitelist.md))
- **同キャラ全個体で `CharacterTemplate` を共有**: Actions / Behaviors / Images / 反転済 RGBA キャッシュ / goja Runtime をテンプレート単位で持ち、Breed で増えてもメモリ・初期化コストが個体数に比例しない
- **Environment は全 Mascot で 1 個共有**: tick 冒頭で 1 回だけ `Refresh` するため、`EnumDisplayMonitors` / `GetCursorPos` / `GetForegroundWindow` 系の syscall コストが N→1 倍に圧縮される

## エントリポイント

[main.go](../main.go) の流れ:

1. CLI フラグ解析: `-name` (キャラ名、空なら `img/` 全キャラ)、`-conf`、`-img`、`-debug`、`-trace-affordance`
2. `runtime.LockOSThread()` でメインスレッド固定
3. SIGINT / SIGTERM → `context.Cancel` のシグナル goroutine を起動
4. `BroadcastRegistry` を 1 個作成し、対象キャラの `CharacterTemplate` をロード
5. 各 template の Action を走査して `TransformMascot` 参照キャラのテンプレートも自動ロード (1 段だけ)
6. 共有 `Environment` を構築・`Refresh`
7. `spawner` を構築し、初期キャラを `spawnCharacter` で起動 (Win32 ウィンドウは仮 1×1 で作成し、最初の `SetBitmap` で実サイズになる)
8. `startTray` で `fyne.io/systray` を別 goroutine 起動
9. `platform.RunMessageLoop` を回す。onTick の中身:
    1. `env.Refresh()` (tick あたり 1 回)
    2. `sp.drainMutations()` (tray / ctx メニューからのリクエストを main thread で消化)
    3. `range sp.chars { c.Tick() }`
    4. `sp.drain()` (`pending` Spawn → `pendingTransform` の順で新規 Character を構築)

## データ構造

### XML 定義 (パース結果)

[mascot/types.go](../mascot/types.go) を参照。要点だけ抜粋:

```go
type Action struct {
    Name       string
    Type       string             // Stay/Move/Animate/Sequence/Select/Embedded
    BorderType string             // Floor/Wall/Ceiling
    Class      string             // Embedded時のFQCN末尾 (Look/Offset/Falling/Jumping等)
    Loop       bool               // Sequence の繰り返し
    Animations []Animation
    Children   []*Action          // インライン Action と解決済 ActionReference を出現順で保持
    Params     map[string]*Evaluator // 式 (${} / #{}) を含む属性

    RefName string                // 解決前 ActionReference のプレースホルダ印 (resolveActionRefs で実体化)

    // Broadcast / ScanMove ハンドシェイク用 (生文字列、式評価しない)
    Affordance         string
    BehaviorAttr       string    // ScanMove: 自分の次 Behavior 名
    TargetBehaviorAttr string    // ScanMove: 到着先 Broadcaster の次 Behavior 名

    // Breed
    BornBehavior string

    // Transform
    TransformMascot   string
    TransformBehavior string     // "TransformBehaviour" もエイリアス
}

type Behavior struct {
    Name          string
    Frequency     int
    Hidden        bool
    Condition     *Evaluator
    NextBehaviors []NextBehavior
    HandlesAir    bool            // 内部で Falling/Jumping を含む → Fall 強制割り込みをスキップ
}

type ConditionGroup struct {
    Condition *Evaluator
    Behaviors []*Behavior
}
```

### 実行時状態

```go
type Mascot struct {
    Name      string
    Anchor    image.Point         // 仮想デスクトップ絶対座標 (Win32 基準)
    LookRight bool
    Velocity  image.Point

    Template *CharacterTemplate   // 共有不変データへの参照 (render が RGBA キャッシュを引く)

    Actions         map[string]*Action
    Behaviors       []*Behavior
    ConditionGroups []ConditionGroup

    CurrentBehavior *Behavior
    CurrentAction   *ActionState
    pendingNext     []BehaviorRef
    pendingReplace  bool

    BehaviorHistory []string      // デバッグ HUD 用 (新しい順)

    // デバッグコールバック (-debug / -trace-affordance で wire される)
    OnBehaviorChange func(name string)
    OnActionEnter    func(name, atype string)
    OnMoveStart      func(name string, target, anchor image.Point)
    OnAffordance     func(event, affordance, detail string)

    Images       map[string]image.Image
    CurrentImage image.Image
    ImageAnchor  image.Point

    Drag       DragState           // None / Started / Holding / Released
    dragOffset image.Point

    tick      int
    vm        *goja.Runtime
    env       *Environment
    sharedEnv bool                 // true なら Tick 内の env.Refresh をスキップ (呼び出し側責務)
    jsScratch *jsScratch           // 使い回し領域 (旧実装の毎 tick map アロケを排除)

    registry   *BroadcastRegistry  // 全 Mascot 共有 (Broadcast/ScanMove)
    forcedNext *BehaviorRef        // ScanMove 到着等で外部から押し込まれる強制遷移

    // activeIE 追従用 (前 tick 終了時にウィンドウのどれかの辺に接していたか)
    onActiveWindow       bool
    prevActiveWindowID   uintptr
    prevActiveWindowRect image.Rectangle

    // 外部ウィンドウグラブ (WalkWithIE / FallWithIE / ThrowIE)
    grabbedHWND   uintptr
    grabbedOffset image.Point      // Win32 SetWindowPos 互換 (左上隅基準)

    // Breed (分裂・召喚)
    spawner      Spawner            // Spawn/Transform リクエストの受け口 (main.go の spawner)
    totalCountFn func() int         // 同キャラの現在個体数 (mascot.totalCount の真値ソース)
    maxCount     int                // <定数 maxCount> の値 (未指定なら defaultMaxCount=10)
}

type ActionState struct {
    Action       *Action
    PoseIndex    int
    PoseTick     int
    SequenceStep int
    ChildState   *ActionState       // Sequence/Select の現在の子
    StartTick    int                // タイムアウト系で使用
    StartAnchor  image.Point
    CachedParams map[string]any     // ${} 評価済み + Broadcast/ScanMove の entry 等の per-action 状態
}
```

### CharacterTemplate (同キャラ共有)

```go
type CharacterTemplate struct {
    Name            string
    Actions         map[string]*Action
    Behaviors       []*Behavior
    ConditionGroups []ConditionGroup
    Images          map[string]image.Image
    Constants       CharacterConstants   // <定数 maxCount="N">

    rgbaCache        map[rgbaCacheKey]*image.RGBA  // (image, lookRight) → premultiplied BGRA
    sharedVM         *goja.Runtime                  // 同キャラ全個体で 1 個
    imagesNormalized map[string]image.Image         // 大文字小文字・スラッシュ統一の正規化キー
}
```

`NewInstance(registry, spawner, totalCountFn, opts)` で個体を生成する。`opts.Env` を渡すと共有 Environment モード (= `Tick` 内の `env.Refresh` をスキップ)。`opts.Anchor` が zero なら `initialSpawnAnchor` (上方モニタ上空ランダム X) を採用。

### Environment

```go
type Environment struct {
    Screens      []image.Rectangle  // 全モニタの WorkArea
    screens      []platform.ScreenInfo // Monitor + WorkArea ペア (含有判定は Monitor)
    Cursor       image.Point
    CursorDelta  image.Point        // 前 tick との差分 (Thrown 初速で使用)
    ActiveWindow platform.ActiveWindow  // ホワイトリスト合致時のみ Visible=true
}

func (e *Environment) Refresh()                            // platform syscall 一括取得
func (e *Environment) CurrentScreen(anchor) image.Rectangle  // anchor を含むモニタの WorkArea
func (e *Environment) CurrentMonitor(anchor) image.Rectangle // 同上の物理範囲
func (e *Environment) WorkAreaUnion() image.Rectangle      // 全モニタ WorkArea の包含矩形 (ThrowIE 境界)
func (e *Environment) IsAtValidPosition(p) bool            // anchor がモニタ近傍の有効範囲内か (リスポーン判定)
func (e *Environment) Floor(anchor) int                    // 画面下端 or activeIE.top のうち近い方
func (e *Environment) Ceiling(anchor) int
func (e *Environment) LeftWall(anchor) int
func (e *Environment) RightWall(anchor) int
func (e *Environment) IsOnFloor(anchor) bool               // 画面床 OR activeIE 上面 (X 範囲内)
func (e *Environment) IsOnCeiling(anchor) bool
func (e *Environment) IsOnWall(anchor) bool
func (e *Environment) IsOnActiveWindow(anchor) bool        // activeIE 上面のみ (歩行可能床判定用)
func (e *Environment) IsAttachedToActiveWindow(anchor) bool // 4 辺いずれか (ウィンドウ追従判定用)
```

`workArea` と `screen` は JS 側で同じ値を返す (shimeji-ee 互換のため別名で公開)。

## Behavior ステートマシン

### 割り込み Behavior (強制実行)

毎 tick の冒頭でチェックし、該当すれば現 Behavior を中断して切り替える。詳細は [mascot/behavior.go](../mascot/behavior.go) `checkInterrupt`。

| 優先度 | イベント                                              | Behavior 名                                                                       |
| ------ | ----------------------------------------------------- | --------------------------------------------------------------------------------- |
| 1      | マウスでドラッグ開始 (`DragStarted`)                  | `Dragged` (forcedNext は破棄、`clearGrab` で外部ウィンドウも解放)                  |
| 1      | ドラッグ解放 (`DragReleased`)                         | `Thrown` (forcedNext は破棄、`CursorDelta.X` の符号で `LookRight` を投擲方向に更新) |
| 2      | `forcedNext` がセット済み (ScanMove 到着等)           | 指定 Behavior を消費して遷移                                                       |
| 3      | 空中にいる (床・壁・天井いずれにも接していない)       | `Fall` (ただし現 Behavior が `HandlesAir=true` ならスキップ)                       |

`HandlesAir` は Action ツリーを走査して Embedded `Fall`/`Falling`/`Jump`/`Jumping` を含む Behavior に自動セットされる ([mascot/behavior.go](../mascot/behavior.go) `markAirHandlers`)。`Fall` / `Thrown` ロールの Behavior は明示的に true。

Broadcast / ScanMove Action 進行中に Drag/Fall 等で中断された場合は `OnAffordance` に `*-interrupted` イベントを送って観測者から「結末不明」にならないようにする。

### 必須 Behavior の役割 (roleAliases)

エンジン側から名前指定で呼び出す Behavior は、英語版 / 旧日本語版の両方の名前で解決できるよう [mascot/roles.go](../mascot/roles.go) で alias テーブル管理する。

| 役割              | 英語版              | 旧日本語版             |
| ----------------- | ------------------- | ---------------------- |
| `ChaseMouse`      | `ChaseMouse`        | `マウスの周りに集まる` |
| `SitAndFaceMouse` | `SitAndFaceMouse`   | `座ってマウスのほうを見る` |
| `Fall`            | `Fall`              | `落下する`             |
| `Dragged`         | `Dragged`           | `ドラッグされる`       |
| `Thrown`          | `Thrown`            | `投げられる`           |

`findBehaviorByRole(role)` が候補名を順に探して最初にヒットしたものを返す。`behaviorMatchesRole(b, role)` は逆方向の判定 (= 現 Behavior が役割に合致するか)。

### 通常抽選

詳細は [xml-format.md](xml-format.md#抽選アルゴリズム) を参照。要点:

1. `pendingReplace=true` なら `pendingNext` のみで決定 (Add="false" 由来)
2. それ以外は: ルート + 該当する `<Condition>` グループ + `pendingNext` (Add="true" 由来) をマージ
3. 各 Behavior の `Condition` 属性で goja フィルタ、`Frequency=0` は通常抽選から除外
4. `Frequency` 加重ルーレット選択
5. NextBehavior 参照経由なら `Frequency=0` でも採用 (重み 0 → 1 に fallback)

## tick 内処理順序

```
[Mascot.Tick]  ([mascot/mascot.go](../mascot/mascot.go))
1. tick++
2. sharedEnv=false ならここで env.Refresh()
3. respawnIfStranded()       — anchor がモニタ範囲外なら teleport + Fall 強制
4. followActiveWindowIfNeeded() — 前 tick で activeIE に乗っていたなら同一ウィンドウの移動量を anchor に反映
5. refreshVM()                — jsScratch.update で goja に渡す mascot オブジェクトを最新化
6. defer updateOnActiveWindowState() — tick 末尾で次 tick 用のウィンドウ追従状態を記録
7. checkInterrupt() → 割り込み発生なら startBehavior
8. CurrentAction == nil なら advanceBehavior()
9. CurrentAction != nil なら StepAction(s, m, env)
   - 完了 (done=true) → CurrentAction=nil
   - forcedNext があれば即消費 (advanceBehavior の通常抽選より優先) して startBehavior
   - 無ければ advanceBehavior()
10. refreshPose() で CurrentImage / ImageAnchor を確定

[main.go: Character.Tick]
11. m.Template.RGBA(src, m.LookRight) で premultiplied BGRA を取得 (キャッシュヒット)
12. ウィンドウ位置 = Anchor − ImageAnchor
13. pose 同一・位置変化のみ → SetBitmap (位置だけ更新)
14. pose 変化 → SetBitmap + SetClickMask、初回なら Show()
```

描画は `UpdateLayeredWindow` 1 回で完結する。`ebiten.Game` のような Update/Draw 2 段階分離は不要。

## Action 実行ロジック

詳細な終了条件は [xml-format.md](xml-format.md#action-タイプの終了条件-実行時) を参照。実装は [mascot/action.go](../mascot/action.go) の `stepXxx` 群。

### ${} と #{} の評価

`${...}` (once) は Action 開始時の `newActionState` で評価して `CachedParams["param:Name"]` にキャッシュ。`#{...}` (per-frame) は毎 tick `Evaluator.EvalValue` で評価。`bindActionParamsToVM` が `pickAnimation` の直前に Action パラメータを VM のグローバル変数 (英語名 + `paramAliasJP` の日本語別名) として set するので、Animation の Condition 式で `TargetY < mascot.anchor.y` のような参照が解決できる。

### Move / Jumping の方向通過判定

`stepMove` は Target に絶対距離で近づいたか ではなく **進行方向側で通過したか** で判定する。Target がランダム抽選で anchor 付近に落ちた場合に 1 tick 終了して連鎖アニメが見えない問題を避けるため。`stepJumping` も同方式。

### Falling の物理

- 物理状態 `(vx, vy, ax, ay)` は `CachedParams` に float で保持 (整数だと小速度で精度が失われる)
- 運動方程式 (本家準拠): `v_new = v_old * (1 - resistance) + gravity (Y のみ)`
- パラメータ: `Gravity` (デフォルト 2.0)、`RegistanceX` / `RegistanceY` (typo そのまま、`Resistance*` もフォールバック)
- 境界バウンドは物理反射ではなく **辺にスナップして即停止** (ビヨンビヨン跳ねる違和感を排除)。バウンドアニメは Thrown/Fall Sequence の `Bouncing` Action (`shime18` / `shime19`) が表現する
- 例外: 壁掴み Behavior (`HoldOntoWall`) を持たないキャラは反射継続 (`wallRestitution=0.5`) で最終的に床着地に流す
- `Thrown` / `Fall` ロール中の壁・天井衝突は `HoldOntoWall` / `HoldOntoCeiling` への確定 forced 遷移 (ルーレットで Climb 系を引いて即終了する問題の回避)
- activeIE 上面のオーバーシュート検出: `vy > 0` で `prev_Y < activeIE.top` から `current_Y >= activeIE.top` を跨いだら snap して着地

### Broadcast / ScanMove ハンドシェイク

shimeji-ee の affordance 連携 (例: Hayate と Nagi が会話する一連の挙動) を **同プロセス内 BroadcastRegistry** で実現する。1 プロセス N キャラ構成のため IPC 不要。

#### 流れ

1. Broadcaster の Behavior が選ばれ、Action `Broadcast Affordance="X"` が開始
2. `stepBroadcast` が `registry.Register("X", broadcaster, pos)` で entry を作成、毎 tick 自分の位置を書き戻し、ポーズを再生
3. ScanMover の Behavior が選ばれ、Action `ScanMove Affordance="X" Behavior="A" TargetBehavior="B"` が開始
4. `stepScanMove` が `registry.FindNearest("X", pos, self)` で entry を取得、その方向へ歩く (Velocity Pose をループ再生)
5. X 通過判定で到着 → `entry.Arrived = true; entry.TargetBehavior = "B"`、自分は `forcedNext = "A"` をセットして Action 完了
6. 次 tick で broadcaster 側 `stepBroadcast` が `entry.Arrived` を検知 → `forcedNext = "B"` をセットして Action 完了
7. 双方 `forcedNext` が `checkInterrupt` (Drag 後・Fall 前) で消費され、指定 Behavior へ強制遷移

#### 失敗条件

- ScanMover が `BorderType="Floor"` を外れる (浮いた / ドラッグされた) → 失敗終了 (通常の NextBehavior 抽選へ)
- Broadcaster がアニメ完走前に終了 (Drag, BorderType 外れ等) → entry を Unregister、ScanMover 側は `entry.Cancelled` を検知して失敗終了
- 同じ Affordance を 2 体以上が Broadcast 中 → ScanMover は最も近い entry を選ぶ (Manhattan 距離)
- 同じ Broadcast に 2 体以上の ScanMover が同時到着 → 先着のみ成立 (`entry.Arrived` で 2 人目以降は弾かれる)
- Broadcast がアニメ完走 (タイムアウト) → entry を Unregister、`broadcast-timeout` イベントをログ

### Breed / Transform (Spawner 経由)

Tick 中に `chars` スライスを変更するとイテレータが壊れるため、生成・破棄系の Action は [mascot/spawner.go](../mascot/spawner.go) `Spawner` インターフェース経由でリクエストをキューに積むだけ。実生成・破棄は `main.go: spawner.drain` で Tick callback 末尾に行う。

```go
type Spawner interface {
    Spawn(req SpawnRequest)         // Breed → 親と同じキャラを (Anchor+BornX, Anchor+BornY) に出現
    Transform(req TransformRequest) // Transform → 自分を別キャラに置き換える
}
```

- Breed: `BornBehavior` を `InitialBehavior` として spawn、`LookRight=true` なら `BornX` を反転 (Pose Velocity と同じ規約)
- Transform: drain で「新キャラ spawn → 旧キャラ destroy」順で処理 (`removeOne` 経路の chars 空 → cancel 誤発火を防ぐ)
- `Spawner=nil` (テスト等) なら no-op で受け流す
- Breed の上限は behaviors.xml の `<定数 Name="maxCount" 値="N" />` (未指定なら `defaultMaxCount=10`)。VM 変数 `maxCount` として公開され、Behavior の Condition `mascot.totalCount < maxCount` でゲートする

### activeIE 追従と即時 Fall

詳細は [window-whitelist.md](window-whitelist.md#ウィンドウ追従) を参照。要点:

- `followActiveWindowIfNeeded` (tick 冒頭): 前 tick で 4 辺いずれかに接していたなら、同一 HWND が依然 visible な場合に `Rect.Min` 差分を anchor に加算
- 乗っていたウィンドウが非表示化 (最小化・クローズ・非ホワイトリストへのフォーカス移動) → 同 tick 内で `forceFallNow` (HandlesAir 判定すらバイパスして Fall 強制遷移、tick 境界の遅延を排除)
- 別ウィンドウへフォーカス移動 (`ID` 不一致) → 即時 Fall は強制せず checkInterrupt の通常空中判定に任せる
- `updateOnActiveWindowState` (tick 末尾、defer): 4 辺いずれかに接していたなら次 tick 追従用に HWND / Rect を保存
- グラブ中 (WalkWithIE/FallWithIE/ThrowIE) は二重移動を避けるため追従ロジックをバイパス

### 外部ウィンドウグラブ (WalkWithIE / FallWithIE / ThrowIE)

詳細は [window-whitelist.md](window-whitelist.md#外部ウィンドウのグラブ-walkwithie--fallwithie--throwie) を参照。実装の要点:

- `ensureGrabbed` で開始時に `activeIE.ID` を確保、`IsExternalWindowGrabbable` チェック (最大化・最小化・破棄済みは false → Action 1 tick 完了)
- `IeOffsetX` / `IeOffsetY` は shimeji-ee 規約 (LookRight=true 向きの左下隅基準) で受けて、内部の `grabbedOffset` (Win32 左上基準) に変換してスナップ
- `driveGrabbedWindow` で毎 tick `MoveExternalWindow(hwnd, anchor.X + offset.X, anchor.Y + offset.Y)` (`SWP_ASYNCWINDOWPOS` 非同期、フォーカス奪わない)
- 2 tick 目以降はユーザの手動ウィンドウ移動を `syncAnchorToGrabbedWindow` で anchor に逆反映 (取り残されない)
- `ThrowIE` は anchor を更新せず独立した `(_wx, _wy)` でウィンドウだけ飛ばす。`Gravity` / `RegistanceX` 等の XML 属性は意図的に無視し、`throwRes=0.03` の慣性減衰で穏やかに止まる
- 完了条件: Action 種別ごとの通常終了 + グラブ中のウィンドウ消失 (`IsExternalWindowAlive=false`) + ThrowIE 固有 (連続 5 tick 低速 / 400 tick 安全装置)
- Drag 割り込み時は `clearGrab` でグラブ解放 (ウィンドウは現在位置に置き去り)

## anchor リスポーン

`respawnIfStranded` (Tick 冒頭、followActiveWindowIfNeeded の前): `env.IsAtValidPosition(anchor)` が false なら `initialSpawnAnchor(env)` (topmost モニタ上空ランダム X) へ teleport + `Fall` ロール強制遷移。モニタ切断、Thrown が画面外で停止、JS 式バグで anchor が異常座標になった際の救済。Drag 中はスキップ (ユーザが意図的に画面外まで掴んでいる可能性がある)。

## 描画・ウィンドウ戦略

### 1 プロセス N キャラ N ウィンドウ

`main.go` がメインスレッドを `runtime.LockOSThread()` で固定し、その上で

- 全キャラの `*mascot.Mascot` を構築
- 各キャラに対応する透過 layered window を `platform.NewWin32Window` で作成
- `platform.RunMessageLoop` で 25 TPS の tick を回しつつ、Win32 メッセージを処理

### ウィンドウサイズ・位置 = `UpdateLayeredWindow` で原子的更新

`UpdateLayeredWindow` は **位置・サイズ・ビットマップ内容をまとめて 1 回で更新**する API なので、pose 切替・移動・反転を 1 syscall で済ませられる。

ウィンドウ位置 = `Anchor - ImageAnchor` (仮想デスクトップ絶対座標)。Win32 はそのまま絶対座標を受け取れるため、ebiten 版で必要だった「モニタ原点減算」は不要。

LookRight=true の水平反転は CPU 側で premultiplied BGRA に変換する際に行い、`(image, LookRight)` ペアごとの RGBA バッファを `CharacterTemplate.rgbaCache` で共有キャッシュする (同キャラ N 体でも pose 数 × 2 枚に圧縮)。

### 透過・最前面・タスクバー非表示

```
WS_POPUP                    : 枠なし
WS_EX_LAYERED               : per-pixel alpha (UpdateLayeredWindow 経路)
WS_EX_TOPMOST               : 最前面
WS_EX_TOOLWINDOW            : タスクバー非表示
WS_EX_NOACTIVATE            : クリックでフォーカスを奪わない
SetWindowRgn(hrgn)          : alpha>0 領域のみクリック対象 (透明部はクリックスルー)
```

クリックスルーは `WS_EX_TRANSPARENT` ではなく `SetWindowRgn` で実現する。`WS_EX_TRANSPARENT` だと不透明部のドラッグも拾えなくなるため。`SetWindowRgn` の領域は alpha チャネルから run-length 圧縮した矩形配列を `ExtCreateRegion` で 1 回だけ作る (CombineRgn ループより速い)。

### コンテキストメニュー (キャラ右クリック)

`WM_RBUTTONDOWN` → `WindowHandlers.OnRightDown` → `spawner.showContextMenu` の経路。`platform.ShowPopupMenu` は `TrackPopupMenu(TPM_RETURNCMD | TPM_RIGHTBUTTON)` のラッパで、選択された ID を同期的に返す。

メニュー項目:

- 「このしめじだけ残す」 → `keepOnly(target)`
- 「もう1匹呼ぶ」 → 同キャラの仲間を 1 体追加 (`spawnCharacter`)
- 「帰ってもらう」 → `removeOne(target)` (chars 空になったら `cancel()`)
- 「アクションを選んで再生」サブメニュー → `PlayActionByName(name)` (同名 Behavior があれば startBehavior、無ければ CurrentAction だけ差し替え)

「アクションを選んで再生」はデバッグ専用ではなく、XML 内の特定アクションをユーザーが直接再生したいケースに応える通常機能として常時表示する。

選択結果は `queueMutation` 経由で main thread のキューに積み、次 tick 冒頭の `drainMutations` で実行する。`DestroyWindow` を作成スレッド以外から呼ぶと失敗するため、変更を必ず main thread に集約する目的。

### システムトレイ

[tray_windows.go](../tray_windows.go) が `fyne.io/systray` を別 goroutine で起動 (`runtime.LockOSThread` で OS スレッドに固定 → systray の Win32 hidden window がメッセージを受けられる)。アイコンは `buna.png` を `pngToICO` で最小 ICO に包んで `SetIcon`。

メニュー:

- 「ふやす」 → ランダムでキャラを 1 体追加
- 「あつまれ！」 → 全キャラをカーソルへ集合
- 「1匹だけのこす」 → `queueMutation(s.keepOnly(nil))` で chars[0] だけ残す
- 「反応するウィンドウ」 → ホワイトリスト toggle サブメニュー (プリセット + ユーザ追加分)
- 「ばいばい」 → `cancel()` + `systray.Quit()`

`onQuit` は tray が外部から閉じられた場合 (例えば Windows のセッション終了) でも呼ばれる。

## メッセージループ

`platform.RunMessageLoop(ctx, tickInterval, onTick)` がメインスレッドで以下を回す:

```
deadline := now
for {
    ctx 確認 → キャンセルなら終了
    PumpMessages() で溜まったメッセージを全消化
    deadline 到達なら onTick() を呼んで deadline += tickInterval
    MsgWaitForMultipleObjects(timeout=次 deadline まで, QS_ALLINPUT) でブロック
}
```

`MsgWaitForMultipleObjects` は「メッセージ到着 or タイムアウト」のどちらか早い方で起きる。`time.Sleep` ベースのループより入力応答が良く、アイドル時の CPU 消費もほぼゼロ。

## platform/ レイヤ

OS 依存処理は Windows 専用。`platform/` は Win32 API 前提で、非 Windows 向けスタブは持たない。

### Windows で使用する API

| API                                                        | 用途                                                  |
| ---------------------------------------------------------- | ----------------------------------------------------- |
| `RegisterClassExW` / `CreateWindowExW` / `DestroyWindow`   | ウィンドウのライフサイクル                            |
| `UpdateLayeredWindow`                                      | 透過描画 (位置・サイズ・bitmap 同時更新)              |
| `CreateDIBSection` / `CreateCompatibleDC`                  | bitmap バッファ作成                                   |
| `ExtCreateRegion` / `SetWindowRgn`                         | クリック領域マスク                                    |
| `SetCapture` / `ReleaseCapture`                            | ドラッグ中のマウス捕捉 (ウィンドウ外でも up を取れる) |
| `MsgWaitForMultipleObjects`                                | メッセージ待ち + tick タイマー                        |
| `EnumDisplayMonitors` / `GetMonitorInfoW`                  | マルチモニタ列挙                                      |
| `GetCursorPos`                                             | カーソル位置取得                                      |
| `GetForegroundWindow` / `GetWindowRect` / `GetClassNameW`  | activeIE 判定                                         |
| `IsWindow` / `IsZoomed` / `IsIconic` / `IsWindowVisible`   | 外部ウィンドウの状態確認                              |
| `SetWindowPos` (`SWP_ASYNCWINDOWPOS`)                      | 外部ウィンドウ移動 (WalkWithIE 等)                    |
| `CreatePopupMenu` / `AppendMenuW` / `TrackPopupMenu`       | コンテキストメニュー                                  |

`x/sys/windows` で公開されていない API は `windows.NewLazySystemDLL("user32.dll").NewProc(...)` で `LazyProc.Call` 経由で手動ラップ。CGo は不使用。

### platform 層の公開 API

```go
package platform

// マルチモニタ
type ScreenInfo struct {
    Monitor  image.Rectangle // 物理範囲 (含有判定用)
    WorkArea image.Rectangle // タスクバー除外
}
func Screens() []ScreenInfo
func CursorPosition() image.Point

// 透過 layered window
type WindowOpts struct {
    Title         string
    X, Y          int
    Width, Height int
    Layered       bool
    Handlers      WindowHandlers
}
type WindowHandlers struct {
    OnLeftDown  func(localX, localY int)
    OnLeftUp    func(localX, localY int)
    OnRightDown func(localX, localY int)
    OnMouseMove func(localX, localY int)
}
func NewWin32Window(opts WindowOpts) (*Win32Window, error)
func (w *Win32Window) SetBitmap(img *image.RGBA, onscreenPos image.Point) error
func (w *Win32Window) SetClickMask(img *image.RGBA) error
func (w *Win32Window) Show()
func (w *Win32Window) Destroy()
func (w *Win32Window) HWND() uintptr

// メッセージループ
func RunMessageLoop(ctx context.Context, tickInterval time.Duration, onTick func()) error
func PumpMessages() bool

// activeIE (ホワイトリスト合致フォアグラウンド)
type ActiveWindow struct {
    Visible   bool
    Rect      image.Rectangle
    ID        uintptr   // HWND
    ClassName string
    Title     string
    Exe       string    // プロセス実行ファイル basename (例: "chrome.exe"), 取得失敗時は空
}
type WindowMatcher struct {
    ClassContains string
    TitleContains string
    ExeEquals     string
}
func GetActiveWindow() ActiveWindow   // 30ms キャッシュ付き
func SetWhitelist(matchers []WindowMatcher)

// 外部ウィンドウ駆動 (WalkWithIE / FallWithIE / ThrowIE)
func MoveExternalWindow(hwnd uintptr, x, y int) error
func IsExternalWindowGrabbable(hwnd uintptr) bool
func IsExternalWindowAlive(hwnd uintptr) bool
func GetExternalWindowRect(hwnd uintptr) (image.Rectangle, bool)

// コンテキストメニュー
type MenuItem struct {
    ID        int
    Label     string
    Submenu   []MenuItem
    Separator bool
    Disabled  bool
}
func ShowPopupMenu(hwnd uintptr, screenX, screenY int, items []MenuItem) int
```

## 終了手段

- システムトレイの「ばいばい」 → `cancel()` で `RunMessageLoop` を抜ける
- Ctrl+C / SIGTERM → 同経路 (`signal.Notify` goroutine が `cancel()` を呼ぶ)
- キャラ右クリックの「帰ってもらう」で全滅 → `removeOne` 内で自動的に `cancel()`
- 全 Character の `Destroy()` は `main` の defer で実行

将来的にはトレイから「特定キャラだけ終了」「Behavior をリセット」等を提供したいが v1 スコープ外。
