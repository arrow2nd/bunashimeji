# activeIE (アクティブウィンドウ) ホワイトリスト仕様

shimeji-ee 互換の `mascot.environment.activeIE.*`
経由でマスコットがフォアグラウンドウィンドウの上に乗ったり、その縁にしがみついたりする機能。元来
Internet Explorer 専用に設計された名前 (`IE`) が残っているが、bunashimeji では
**任意の許可リスト合致ウィンドウ** を意味する。

## 動作

1. 毎 tick (40ms) `GetForegroundWindow` で現在のフォアグラウンドウィンドウ HWND
   を取得
   - 30ms キャッシュ付き (1 tick 内の N 体の `env.Refresh()` 重複呼び出しを吸収)
2. 自プロセスのマスコットウィンドウ (class `BunashimejiWindow`) は除外
3. 最小化中 (`IsIconic`) や非表示 (`!IsWindowVisible`) は除外
4. ホワイトリストと class 名 / タイトル を case-insensitive 部分一致で照合
5. 合致なら `GetWindowRect` で矩形を取り `mascot.environment.activeIE`
   として公開、合致しなければ `visible=false`

ホワイトリストを使うことで「ブラウザの上には乗ってほしいが、自分の Slack
ウィンドウには乗ってほしくない」のような調整が可能。

## JS API (XML 条件式から見える形)

```js
mascot.environment.activeIE.visible; // bool: ホワイトリスト合致 ＆ フォアグラウンド ＆ 非最小化
mascot.environment.activeIE.isVisible; // bool: visible のエイリアス (互換)
mascot.environment.activeIE.left; // int : 仮想デスクトップ座標
mascot.environment.activeIE.top; // int
mascot.environment.activeIE.right; // int
mascot.environment.activeIE.bottom; // int
mascot.environment.activeIE.width; // int
mascot.environment.activeIE.height; // int
mascot.environment.activeIE.topBorder.isOn(anchor); // bool: anchor が上辺に接しているか
mascot.environment.activeIE.bottomBorder.isOn(anchor); // bool
mascot.environment.activeIE.leftBorder.isOn(anchor); // bool
mascot.environment.activeIE.rightBorder.isOn(anchor); // bool
```

`isOn(anchor)` は接触許容 (`borderTolerance=2px`) かつ X/Y 範囲内の場合に
true。`visible=false` の場合は全 `isOn` が常に false を返すので、Behavior の
Condition 評価が自然にスキップされる。

## 物理サポート (どの境界に「乗れる」か)

JS API としては全 4 辺の `isOn` が公開されているが、**物理的な衝突判定**
(Falling 着地・Walk 継続条件) は現時点で **上面 (`topBorder`) のみ**
実装されている。

| 境界           | JS `isOn` | 物理衝突 (Falling/Walk)                                                                                                    | 状態                  |
| -------------- | --------- | -------------------------------------------------------------------------------------------------------------------------- | --------------------- |
| `topBorder`    | ✅        | ✅ ([mascot/environment.go](../mascot/environment.go) Floor/IsOnFloor + [stepFalling](../mascot/action.go) overshoot 検出) | 実装済                |
| `bottomBorder` | ✅        | ❌                                                                                                                         | TODO (天井ぶら下がり) |
| `leftBorder`   | ✅        | ❌                                                                                                                         | TODO (側面登り)       |
| `rightBorder`  | ✅        | ❌                                                                                                                         | TODO (側面登り)       |

つまりマスコットは**ウィンドウの上に着地・歩行**できるが、サイドにジャンプして登る・底辺にぶら下がるといった挙動は
v1 では起こらない (該当 Behavior
が選ばれても物理判定が通らないので即終了する)。要望次第で側面・底辺サポートも
`IsOnWall` / `IsOnCeiling` の拡張で追加可能。

### 上面着地のロジック

`stepFalling` (落下) は次の 2 段階で activeIE 上面を扱う:

1. **オーバーシュート検出**: 1 step で `prev_Y < activeIE.top` から
   `current_Y >= activeIE.top` に跨いだ場合、`activeIE.top`
   にスナップしてバウンス処理 ([mascot/action.go](../mascot/action.go)
   stepFalling 内)
2. **通常床判定**: `Floor(anchor)` が `anchor.X` が activeIE X 範囲内かつ
   `anchor.Y <= activeIE.top` のとき `activeIE.top`
   を返すので、既存の床着地パスがそのまま着地点として使う

`Walk` 等の `BorderType="Floor"` Action は `IsOnFloor(anchor)`
を見て継続/終了判定する。`IsOnFloor` は画面床と activeIE
上面のどちらかに接していれば true を返す。

### ウィンドウ追従

activeIE のいずれかの辺 (top / bottom / left / right)
にくっついているマスコットは、ウィンドウ自体の移動 (ユーザーがドラッグした等) に
**自動追従** する。上面歩行だけでなく、`JumpOnIELeftWall` などで側面/底面に
`GrabWall` でしがみついている間も追従する。

実装 ([mascot/mascot.go](../mascot/mascot.go) `followActiveWindowIfNeeded`):

- 各 Mascot は前 tick 終了時の状態 (`onActiveWindow` / `prevActiveWindowID` /
  `prevActiveWindowRect`) を保持
- くっつき判定は [mascot/environment.go](../mascot/environment.go)
  `IsAttachedToActiveWindow` で 4 辺すべてを許容 (上面のみの判定
  `IsOnActiveWindow` は歩行可能床判定 `IsOnFloor` 用に別途残してある)
- 次 tick 冒頭で、まだ同じウィンドウ (ID 一致) が visible なら、`Rect.Min`
  の差分 (dx, dy) を anchor に加算
- ID が変わった (別ウィンドウにフォーカスが移った) 場合は追従しない →
  マスコットは直前位置に残り、次の `checkInterrupt` で Fall 割り込みされて落下
- Drag 中 (ユーザーがマスコットを掴んでいる) は追従しない (掴まれた手を優先)

ウィンドウ追従の条件には `ActiveWindow.ID` (Win32 では HWND)
を使うので、タイトルが変わっただけ (Chrome のタブ切替等)
は同一ウィンドウ扱いで追従が継続する。

### ウィンドウ非表示時の即時落下

乗っていたウィンドウが **非表示になった瞬間**
(最小化・クローズ・非ホワイトリストアプリへのフォーカス移動など
`aw.Visible=false` になるケース) は、`followActiveWindowIfNeeded` 内で **同 tick
内に** 直接 `Fall` Behavior へ切り替える
([mascot/mascot.go](../mascot/mascot.go) `forceFallNow`)。

これをやらず checkInterrupt の通常空中判定に任せると、Fall 発動が次 tick (≒ 40ms
後) になりマスコットが空中に「浮いた」ように見える間が生じる。即時切替により
tick 境界の遅延を解消する。

注意:

- 元 Behavior が `HandlesAir=true` (Falling/Jumping を内包) でも Fall
  を優先する。物理的な足場が消えた以上、ジャンプ系の継続より落下を選ぶ
- 別ウィンドウへフォーカスが移った場合 (`ID` 不一致)
  は元ウィンドウがまだ画面に残っているかもしれないので即時 Fall
  は強制せず、checkInterrupt の通常判定に任せる (1 tick 遅れ)

## 出荷時プリセット

[platform/preset.go](../platform/preset.go) の `Presets()`
にハードコード。「投げて遊んでも作業に支障の出にくい」アプリだけに絞ってある。

| ID            | Label               | マッチ条件                                                     | 備考                             |
| ------------- | ------------------- | -------------------------------------------------------------- | -------------------------------- |
| `chrome`      | Google Chrome       | exe: `chrome.exe`                                              |                                  |
| `edge`        | Microsoft Edge      | exe: `msedge.exe`                                              |                                  |
| `firefox`     | Firefox             | exe: `firefox.exe`                                             |                                  |
| `brave`       | Brave               | exe: `brave.exe`                                               |                                  |
| `vivaldi`     | Vivaldi             | exe: `vivaldi.exe`                                             |                                  |
| `opera`       | Opera               | exe: `opera.exe`                                               |                                  |
| `calc`        | 電卓                | class: `ApplicationFrameWindow` + title: `電卓`                | UWP 共通 class なので title 必須 |
| `stickynotes` | 付箋                | class: `ApplicationFrameWindow` + title: `付箋`                |                                  |
| `photos`      | フォト              | class: `ApplicationFrameWindow` + title: `フォト`              |                                  |
| `mediaplayer` | メディア プレーヤー | class: `ApplicationFrameWindow` + title: `メディア プレーヤー` |                                  |
| `notepad`     | メモ帳              | class: `Notepad`                                               | 旧版・新版共通                   |
| `paint`       | ペイント            | class: `MSPaintApp`                                            | クラシック版のみ                 |

ブラウザを **class ではなく exe** でマッチする理由: `Chrome_WidgetWin_1`
を使うと VS Code / Slack / Discord / Obsidian 等の Electron
アプリ全部が対象になってしまい、「作業中のアプリに乗られたくない」という出荷時の方針と矛盾するため。exe
ベースなら Edge `msedge.exe` と VS Code `Code.exe` を確実に区別できる。

Terminal 系 (`CASCADIA_HOSTING_WINDOW_CLASS` / `ConsoleWindowClass`)
は意図的に除外。コマンド実行中にウィンドウを動かす事故を避けるため、欲しい場合は
`windows.json` でユーザが明示的に追加する。

## マッチング規則

`WindowMatcher` は以下の構造:

```go
type WindowMatcher struct {
    ClassContains string  // 空文字列なら無視
    TitleContains string  // 空文字列なら無視
    ExeEquals     string  // 空文字列なら無視
}
```

- 複数フィールド指定 → AND マッチ (例: `class=ApplicationFrameWindow` +
  `title=電卓`)
- 全フィールド空 → 無効エントリ (誤って全マッチさせない安全策)
- `ClassContains` / `TitleContains` は **case-insensitive 部分一致**
  (`strings.Contains` を `ToLower` 後に適用)
- `ExeEquals` は **case-insensitive 完全一致** (basename, 例: `chrome.exe`)

`ExeEquals` の値は HWND から `GetWindowThreadProcessId` +
`OpenProcess(QUERY_LIMITED_INFORMATION)` + `QueryFullProcessImageNameW`
で取得した実行ファイルパスの basename と比較する。UAC 越え等で `OpenProcess`
が失敗した場合は exe 名が空になり、`ExeEquals` 指定エントリにはマッチしない
(class/title 指定エントリには通常通り評価される)。

## 設定ファイル: conf/windows.json

ユーザがプリセットを ON/OFF したり、独自エントリを追加するための JSON。

### パス

既定: `conf/windows.json` (CLI の `-conf` で `conf` を変えれば追随) 明示指定:
`bunashimeji.exe -windows-config <path>`

ファイルが存在しなくても起動は止めない。全プリセット有効・ユーザ追加無しの状態で動く。

### フォーマット

```json
{
  "windows": [
    { "label": "自作ツール", "exe": "mytool.exe" },
    { "label": "Slack", "exe": "slack.exe", "enabled": false },
    { "label": "Skype", "class": "ApplicationFrameWindow", "title": "Skype" }
  ],
  "disabledPresets": ["firefox", "opera"]
}
```

| キー                | 型     | 意味                                              |
| ------------------- | ------ | ------------------------------------------------- |
| `windows[]`         | array  | ユーザ追加分エントリのリスト                      |
| `windows[].label`   | string | tray メニューに表示する名前 (必須)                |
| `windows[].exe`     | string | 実行ファイル basename 完全一致 (例: `chrome.exe`) |
| `windows[].class`   | string | クラス名部分一致                                  |
| `windows[].title`   | string | タイトル部分一致                                  |
| `windows[].enabled` | bool   | 省略時 `true`。明示 `false` で無効化              |
| `disabledPresets[]` | array  | 無効化したプリセット ID のリスト                  |

`exe` / `class` / `title` のいずれか 1 つ以上が必須。複数指定で AND マッチ。

ファイルは tray の toggle 操作で **アプリ側から書き戻される**。コメント (`//`)
は標準 JSON
仕様外なので使えない。ユーザが手編集する場合は単純なエディタで開く想定。原子的書き換え
(`tmp` + `rename`) で部分書き込みは防いでいる。

## tray UI

タスクトレイの「反応するウィンドウ」サブメニューに、プリセット + `windows.json`
の `windows[]` 全件が並ぶ。各項目はチェックボックスで個別に ON/OFF できる。

```
ぶなしめじ
├── ふやす
├── あつまれ！
├── 1匹だけのこす
├── 反応するウィンドウ ▶  [✓] Google Chrome
│                         [✓] Microsoft Edge
│                         [ ] Firefox
│                         ...
│                         ─ 自作ツール (← ユーザ追加分の区切りマーカー)
│                         [✓] 自作ツール
├──────────────
└── ばいばい
```

- toggle 即座に `windows.json` へ保存、`platform.SetWhitelist` で実マッチも更新
  (再起動不要)
- プリセットの toggle は `disabledPresets[]` を編集
- ユーザ追加分の toggle は `windows[].enabled` を編集
- JSON に新規エントリを追加した場合は **アプリ再起動が必要** (tray
  は起動時のスナップショットで構築)

### マウスホバー時の tooltip

各項目は hover
で「`exe: chrome.exe`」のような短い説明を出す。マッチ条件のうち最も特徴的なフィールド
1 つを表示する。

## 外部ウィンドウのグラブ (WalkWithIE / FallWithIE / ThrowIE)

shimeji-ee 日本語版固有の Embedded Action である `WalkWithIE` / `FallWithIE` /
`ThrowIE` は、マスコットがホワイトリスト合致ウィンドウを
**物理的に掴んで動かす**。activeIE が「読み取り
(乗る・歩く)」だったのに対し、これらは「書き込み (ウィンドウを動かす)」になる。

### 動作

1. Action 開始時に `activeIE.ID` (HWND) と相対オフセットをグラブ状態として確保
   - `IeOffsetX` / `IeOffsetY` 属性が **XML
     で指定されていれば**、それを「**LookRight=true
     向きでの**ウィンドウ左下隅から mascot.Anchor までの差」(shimeji-ee 規約)
     として解釈し、**スナップ**する
   - X は LookRight に応じて反転 (= `BornX` と同じ規則):
     - `LookRight=true` (右向き): `window.left = mascot.x + IeOffsetX` → mascot
       は window の左下隅
     - `LookRight=false` (左向き): `window.right = mascot.x - IeOffsetX` →
       mascot は window の右下隅
   - Y は LookRight で反転しない: `window.bottom = mascot.y + IeOffsetY` 常に
   - 内部 `grabbedOffset` は Win32 SetWindowPos 互換 (左上隅基準)
     で保持するので、入力時に変換: `LookRight=true` なら
     `offset.X = IeOffsetX`、`LookRight=false` なら
     `offset.X = -IeOffsetX - winW`。`offset.Y = IeOffsetY - winH` 常に
   - 未指定なら `(activeIE.Rect.Min - mascot.Anchor)` を **動的計算**
     してそのまま採用 (= 開始時点の見た目を維持、スナップなし)
2. 掴めない条件 (最大化中・最小化中・破棄済み・UAC 越え) では Action を 1 tick
   で完了 → 通常の Behavior 抽選にフォールバック
3. 掴めたら毎 tick
   `MoveExternalWindow(hwnd, anchor.X + offset.X, anchor.Y + offset.Y)` で
   `SWP_NOSIZE | SWP_NOZORDER | SWP_NOACTIVATE | SWP_ASYNCWINDOWPOS`
   を立てて移動 (フォーカスを奪わない・非同期で投げる)
4. WalkWithIE / FallWithIE の 2 tick 目以降は `syncAnchorToGrabbedWindow`
   でユーザの手動ウィンドウドラッグ分を逆方向に anchor へ反映
   (マスコットが取り残されない)。ウィンドウ取得に失敗したら Action 終了
5. Action 終了時 (TargetX 到達・床着地・速度減衰・ウィンドウ消失) にグラブ解放
6. グラブ中は activeIE 追従ロジック (`followActiveWindowIfNeeded`)
   をバイパスして二重移動を防止

### IeOffsetX / IeOffsetY の使い分け

| パターン                                          | 用途                                                                                                                                                                                                                                                                                                                                          |
| ------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `IeOffsetX="0" IeOffsetY="-64"` (shimeji-ee 標準) | LookRight=true (右へ歩く・投げる) で mascot が **window の左下隅** 64px 下に、LookRight=false (左へ歩く・投げる) で **window の右下隅** 64px 下にスナップ。`ImageAnchor="64,128"` (足元) + `Jumping TargetX=activeIE.left or right TargetY=activeIE.bottom+64` と組み合わせると、ウィンドウは元の位置から動かずマスコットだけが下隅に張り付く |
| 指定なし                                          | 前段 Action から続く「すでに掴んでいる相対位置」を保つ。`Sequence` で `WalkWithIE` (IeOffset 指定) → `ThrowIE` (指定なし) と連鎖した場合、ThrowIE が抱え位置を継承する                                                                                                                                                                        |

### フォーカス変化に対する挙動

グラブ中に別ウィンドウへフォーカスが移った場合 (ユーザが他アプリをクリック等)
でも、開始時の HWND を握り続ける。Action 終了までは activeIE
の追跡から独立して駆動するため、フォーカス奪取で挙動が途切れない。

### ThrowIE の挙動

**マスコットと切り離して投げる**: ThrowIE 開始時の
(`mascot.Anchor + grabbedOffset`) をウィンドウの初期位置として独立した状態
`(_wx, _wy)`
に保存し、以後マスコット位置は更新せず**ウィンドウだけが物理運動**する。`Sequence`
で `WalkWithIE → ThrowIe → Stand → Look → Stand` と連鎖した場合、ThrowIe 後の
Stand はマスコットの投げた位置 (床上) でそのまま再開する。

**境界**: 全モニタ WorkArea の連合矩形 (`Environment.WorkAreaUnion()`)
を使う。マルチモニタ環境ではウィンドウがモニタ間を自由に飛び越え、最外周でバウンドする。シングルモニタ環境では
`CurrentScreen` と同一結果。

**物理**: 初速 `InitialVX` / `InitialVY` (XML 評価) で出発した後、毎 tick
`vx *= 1 - throwResX` / `vy *= 1 - throwResY` (`throwResX = throwResY = 0.03`)
の慣性減衰で進む。ウィンドウ矩形 (左上 `_wx,_wy` ~ 右下 `_wx+winW, _wy+winH`)
がいずれかの境界を超えたら、その軸の速度を `-v * 0.4` で反転 +
境界へクランプ。さらに**直交軸の速度を 0.7 倍**して減衰させる (床着地時に X
方向の運動エネルギーも食う = 滑りすぎ防止)。

shimeji-ee オリジナル XML の `Gravity` / `RegistanceX`
等は意図的に**無視**する。本家準拠の放物線落下だとウィンドウがバウンドし続けて落ち着かない違和感が強いため、慣性のみで穏やかに止まる挙動に統一した。

**停止条件**:

- 連続 5 tick で `|vx| < 2.0 && |vy| < 2.0` → 静止判定で完了
- 400 tick (= 16 秒) 超過で安全装置として強制完了

### Drag 割り込み時の解放

ユーザがマスコットを掴んだ瞬間 (Drag 開始時) と離した瞬間 (Drag 解放時) の両方で
`clearGrab()` が呼ばれ、グラブ中のウィンドウは現在位置に置き去られる。Dragged /
Thrown Behavior に強制遷移するため。

### 制約

- **最大化ウィンドウは掴めない**: `IsZoomed`
  で弾く。ユーザの作業状態を壊さないため `ShowWindow(SW_RESTORE)`
  で復元してから掴むことはしない
- **UAC 昇格プロセスのウィンドウも掴めない**: `SetWindowPos`
  がサイレントに失敗するので呼び出し直後にエラー検知して Action 終了
- **DPI スケーリング**: 下記の活性 IE と同じ既知制限 (`SetWindowPos`
  の座標も物理ピクセル前提)
- **`SetWindowRgn` で形作られた非矩形ウィンドウ**: 矩形扱いで move
  するので見栄え通りには動かない場合あり (実害は軽微)

## 制限事項

- DPI スケーリング: `GetWindowRect` は物理ピクセル座標を返す。マルチ DPI
  環境で誤差が出る可能性あり (実害が出たら
  `DwmGetWindowAttribute(DWMWA_EXTENDED_FRAME_BOUNDS)` への切替を検討)
- フルスクリーンアプリ (ゲーム等): 通常の `GetWindowRect`
  は取れるが、上に乗る挙動は描画レイヤの関係で見えない場合がある
- 非 Win32 ウィンドウ (Java Swing 等の特殊実装): class
  名が動的に変わる場合があり一致しないことがある

