# bunashimeji 設計ドキュメント

## プロジェクト概要

shimeji-ee (Kilkakon版) 互換のデスクトップマスコットを Go で自作する。Java
ランタイム不要のシングルバイナリ配布を実現することがコアゴール。既存の非 Java
実装である Shijima-Qt はアーカイブ済みで保守継続が見込めないため、新規実装する。

- **モジュールパス:** `github.com/arrow2nd/bunashimeji`
- **対応フォーマット:** shimeji-ee (Kilkakon版) の actions.xml / behaviors.xml +
  オリジナルの 行動.xml / 動作.xml
- **画像規約:** `shime1.png 〜 shime46.png` 形式（キャラごとに揃っている前提）
- **ターゲットOS:** Windows 優先、macOS は将来対応 (現状はスタブ)
- **配布形式:** シングルバイナリ

## 技術スタック

| 用途                           | 採用                                                                                                              |
| ------------------------------ | ----------------------------------------------------------------------------------------------------------------- |
| 描画・ウィンドウ・ゲームループ | Win32 API 直叩き (`UpdateLayeredWindow` + `MsgWaitForMultipleObjects` + `SetWindowRgn`)                           |
| Win32 API バインディング       | `golang.org/x/sys/windows` + 手動 syscall (CGo なし)                                                              |
| タスクバー常駐 (system tray)   | [`fyne.io/systray`](https://github.com/fyne-io/systray) (Windows ビルドのみ; 非 Windows は no-op スタブ)          |
| 条件式評価                     | [`github.com/dop251/goja`](https://github.com/dop251/goja) (JavaScript エンジン、同キャラ全個体で 1 Runtime 共有) |
| XML パース                     | `encoding/xml` (標準ライブラリ)                                                                                   |
| PNG デコード                   | `image/png` (標準ライブラリ)                                                                                      |

設計制約:

- **CGo を持ち込まない**: クロスコンパイルとシングルバイナリ配布を維持
- **GUI ライブラリに依存しない**: ebiten 等の game engine 系は使わない。25 TPS
  で 1 ウィンドウ N 体スプライト程度の負荷に対し GPU
  レンダリングは過剰で、プロセスあたりのメモリオーバーヘッドが大きい
- **1 プロセス N キャラ N ウィンドウ**: shimeji-ee Java
  版と同じ構成。Broadcast/ScanMove のような cross-mascot 連携を IPC
  なしで実現する
- **同キャラ複数体は CharacterTemplate を共有**: XML/PNG パース結果・goja
  Runtime・反転済 RGBA バッファをテンプレート単位でキャッシュし、Breed
  等で同キャラを増やしてもメモリ・初期化コストを増やさない

## ディレクトリ構成

### プロジェクトソース

```
bunashimeji/
├── main.go                       # エントリポイント: 全キャラを単一プロセスで束ねる
│                                 # spawner / Character / ctx メニュー / tray ブリッジを束ねる
├── tray_windows.go               # システムトレイ (fyne.io/systray) を別 goroutine で起動
├── tray_other.go                 # 非 Windows 向け no-op スタブ
├── mascot/
│   ├── mascot.go                 # Mascot 構造・Tick・割り込み・ウィンドウ追従・グラブ駆動
│   ├── types.go                  # Action / Behavior / Pose / ActionState 等の型定義
│   ├── template.go               # CharacterTemplate: 同キャラ全個体で共有する不変データ
│   │                             #   (Actions/Behaviors/Images, 共有 goja Runtime, RGBA キャッシュ)
│   ├── render.go                 # 反転 + premultiplied BGRA 変換 (Template.RGBA キャッシュ)
│   ├── spawner.go                # Spawner インターフェース (Spawn / Transform リクエスト型)
│   ├── behavior.go               # Behavior ステートマシン (抽選 / 割り込み / forcedNext)
│   ├── roles.go                  # 必須 Behavior の役割名 → エイリアス解決 (英語版 / 日本語版)
│   ├── action.go                 # Action 実行 (Stay/Move/Animate/Sequence/Select + 全 Embedded)
│   ├── registry.go               # BroadcastRegistry: 同プロセス内 Broadcast/ScanMove ハンドシェイク
│   ├── environment.go            # 環境情報・境界判定・activeIE 追従ロジック
│   ├── jsscratch.go              # goja に渡す mascot オブジェクトの使い回し領域
│   ├── expr.go                   # ${} / #{} の式評価ラッパ
│   ├── xml.go                    # actions.xml / behaviors.xml のパース、ActionReference 解決
│   └── xml_legacy.go             # 旧日本語版 XML の属性名・要素名を英語版へ正規化
└── platform/
    ├── window_windows.go         # Win32 透過 layered window + メッセージループ + モニタ列挙
    ├── window_darwin.go          # macOS スタブ (errors.New を返す)
    ├── active_window_windows.go  # GetForegroundWindow + ホワイトリストマッチ + exe 名取得 + 外部ウィンドウ移動
    ├── active_window_darwin.go   # macOS スタブ
    ├── window_whitelist.go       # conf/windows.json のロード/セーブ、プリセット + ユーザ追加の統合ビュー、tray 用 toggle API
    ├── preset_windows.go         # 「投げて遊べる」アプリのプリセット一覧 (ブラウザ・電卓・メモ帳等)
    ├── preset_darwin.go          # macOS スタブ (空)
    ├── menu_windows.go           # TrackPopupMenu ラッパ (ctx メニュー)
    └── menu_darwin.go            # macOS スタブ (常に 0 を返す)
```

### 実行時アセット (実行ファイルと同階層)

```
bunashimeji.exe
conf/
├── actions.xml              # デフォルト
├── behaviors.xml
└── [キャラ名]/
    ├── actions.xml          # キャラ固有 (大文字小文字揺れあり)
    └── behaviors.xml
img/
├── [キャラ名]/              # キャラ画像群
└── unused/                  # 無視するフォルダ
```

キャラクター一覧は `img/` 直下サブディレクトリ (`unused` を除く) で決定する。

## v1 スコープ

### 実装済み

- Action タイプ: **Stay / Move / Animate / Sequence / Select**
- Embedded Action: **Falling / Jumping / Look (Turn) / Offset (Move) / Dragged /
  Broadcast / ScanMove / Breed / WalkWithIE / FallWithIE / ThrowIE / Transform**
  - `Regist` / `Interact` / `BroadcastPosition` / `Sound` は受け流し (no-op)
- BorderType: **Floor / Wall / Ceiling** (画面端のみ)
- Behavior 全般 (旧日本語版 XML の属性・要素名は `xml_legacy.go`
  で英語版に正規化して扱う)
- マウスドラッグ操作 (カーソル直接追従、解放時に投げ方向で LookRight を更新)
- マルチスクリーン対応 (`EnumDisplayMonitors` で全モニタの Monitor / WorkArea
  を取得)
- **Broadcast / ScanMove 連携** (同プロセス内 BroadcastRegistry
  経由のハンドシェイク)
- 単一プロセスで N キャラ並行実行 (`-name X` で 1 キャラ起動、未指定なら `img/`
  全キャラ起動)
- **Breed (分裂・召喚)** — `SplitIntoTwo` / `PullUpShimeji` / `Anzu うさぎ放置`
  等。`<定数 Name="maxCount" 値="N" />` でキャラ別に上限制御 (未指定は
  `defaultMaxCount=10`)
- **外部ウィンドウグラブ** — `WalkWithIE` / `FallWithIE` / `ThrowIE`。activeIE
  ホワイトリスト合致ウィンドウを `SetWindowPos` で動かす。詳細は
  [window-whitelist.md](window-whitelist.md#外部ウィンドウのグラブ-walkwithie--fallwithie--throwie)
- **activeIE ホワイトリストの設定 UI** — exe 名 / class / title
  でマッチ、`conf/windows.json` を設定ファイルとして読み込み、tray
  メニュー「反応するウィンドウ」サブメニューでプリセット
  (ブラウザ・電卓・メモ帳等) と user 追加分を個別にチェックボックス ON/OFF
  できる。詳細は
  [window-whitelist.md](window-whitelist.md#設定ファイル-confwindowsjson)
- **Transform (キャラ姿変化)** — `TransformMascot` / `TransformBehavior`
  (英国綴り `TransformBehaviour` もエイリアス)
  属性で別キャラへ置換。`Spawner.Transform` 経由で旧 Character
  を破棄し新キャラを同 anchor で spawn。`-name X` 起動時に X が Transform
  を含めば変身先テンプレートも自動ロード (推移閉包は追わず 1 段のみ)
- **システムトレイ常駐** — `fyne.io/systray` 経由。アイコンは `buna.png` を最小
  ICO に包んで使う。メニュー:
  「ふやす」「あつまれ！」「1匹だけのこす」「反応するウィンドウ」(ホワイトリスト
  toggle サブメニュー)・「ばいばい」
- **キャラ右クリックメニュー** — `TrackPopupMenu`
  直叩き。「このしめじだけ残す」「もう1匹呼ぶ」「帰ってもらう」「アクションを選んで再生」(全
  Action 名をアルファベット順サブメニュー)。chars / window 破棄は tick
  冒頭のミューテーションキュー経由で main thread に集約する
- **CLI フラグ** — `-name`, `-conf` (既定 `conf`), `-img` (既定 `img`),
  `-windows-config` (既定 `[conf]/windows.json`), `-debug` (Behavior/Action
  遷移ログ), `-trace-affordance` (Broadcast/ScanMove イベントのみログ)
- **共有 Environment / 共有 goja Runtime** — `env.Refresh()` は tick あたり 1
  回、`platform syscall` を個体数 N に対して N→1 に圧縮。Runtime
  も同キャラ全個体で 1 個に共有 (個体コンテキストは `mascot` グローバルを毎 tick
  上書きして切替)
- **anchor リスポーン** — モニタ範囲外に飛んだ anchor は `respawnIfStranded` で
  `initialSpawnAnchor` (最上段モニタ上空) へ teleport + Fall 強制
