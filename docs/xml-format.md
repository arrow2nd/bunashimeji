# XML フォーマット仕様

shimeji-ee (Kilkakon版) の `actions.xml` / `behaviors.xml` をそのまま読み取る。Java 実装の挙動と互換であることが要件。

## 共通: ルート構造

両ファイルともルートは `<Mascot>` 要素で、名前空間 `http://www.group-finity.com/Mascot` を持つ。

```xml
<?xml version="1.0" encoding="UTF-8" ?>
<Mascot xmlns="http://www.group-finity.com/Mascot"
        xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"
        xsi:schemaLocation="http://www.group-finity.com/Mascot Mascot.xsd">
  <!-- ActionList または BehaviorList -->
</Mascot>
```

要素名・属性値に**日本語が混じる**ことが普通にある (例: `Behavior Name="うさぎ放置"`, `Action Name="あくび"`)。UTF-8 で素直に扱う。

### 旧日本語版 XML の正規化

shimeji-ee オリジナル日本語版は要素名・属性名・列挙値まで日本語化された XML を採用する (`<マスコット>`, `<動作>`, `種類="移動"`, etc.)。`stripNamespace` 後にルート要素が `<マスコット>` なら旧形式と判定し、[mascot/xml_legacy.go](../mascot/xml_legacy.go) の正規化テーブルで英語版互換に変換してからパースする。変換は要素名・属性名・`Type` / `BorderType` の値のみで、`Name` 属性値・テキストノード・条件式の中身は**触らない** (例: `<BehaviorReference Name="マウスの周りに集まる">` の `Name` 値はそのまま)。

条件式の本文中に残った日本語パラメータ参照 (例: `目的地Y < mascot.anchor.y`) は、`bindActionParamsToVM` が英語パラメータと同じ値を日本語別名でも VM に公開することで解決する (`paramAliasJP` 表)。

## 読み込み手順

### 1. 名前空間宣言を除去してからパースする

`encoding/xml` の名前空間扱いがハマりやすいので、生バイト列から名前空間属性を削除してからデコードする。

```go
data = bytes.ReplaceAll(data, []byte(` xmlns="http://www.group-finity.com/Mascot"`), []byte(""))
```

### 2. キャラ固有 XML の優先順位

```
conf/[Name]/actions.xml   →  存在すればこちら
conf/actions.xml          →  なければデフォルト
```

`behaviors.xml` も同様。

### 3. ファイル名は case-insensitive で探索

実在する shimeji-ee 配布物には `Actions.xml` / `actions.xml` / `Behavior.xml` (単数) / `動作.xml` / `行動.xml` (旧日本語版) 等の揺れがある。ディレクトリを列挙して大文字小文字を無視で照合すること。実装の候補リストは [mascot/xml.go](../mascot/xml.go) `resolveConfPath` を参照。

### 4. キャラクター一覧

`img/` 直下サブディレクトリ。`unused` ディレクトリは除外する。

## actions.xml

### 構造

```xml
<Mascot ...>
  <ActionList>
    <Action Name="..." Type="..." BorderType="..." Class="..." [パラメータ属性...]>
      <Animation [Condition="..."]>
        <Pose Image="/path.png" ImageAnchor="80,160" Velocity="-2,0" Duration="6" />
        ...
      </Animation>
      <!-- Sequence/Select の場合は ActionReference を持つ -->
      <ActionReference Name="..." [上書きパラメータ...] />
    </Action>
  </ActionList>
</Mascot>
```

### Action 属性

| 属性 | 説明 |
|---|---|
| `Name` | 一意な識別子 |
| `Type` | `Stay` / `Move` / `Animate` / `Sequence` / `Select` / `Embedded` |
| `BorderType` | `Floor` / `Wall` / `Ceiling` (該当境界に接している時に有効) |
| `Loop` | `Sequence` で `true` なら全 step 完走後に先頭に戻る (ループ) |
| `Class` | `Type="Embedded"` のとき必須。`com.group_finity.mascot.action.Look` 等の Java FQCN。bunashimeji では末尾だけ取り出して dispatch する。実装サポート: `Look` (`Turn` 別名) / `Offset` (`Move` 別名) / `Falling` (`Fall` 別名) / `Jumping` (`Jump` 別名) / `Dragged` / `Broadcast` / `ScanMove` / `Breed` / `WalkWithIE` / `FallWithIE` / `ThrowIE` / `Transform`。`Regist` / `Interact` / `BroadcastPosition` / `Sound` は no-op 受け流し。未知 Class は警告ログ後 Animate と同じく Pose を完走させて受け流す |
| `Affordance` | `Class="Broadcast"` / `Class="ScanMove"` のとき使用するハンドシェイクキー (生文字列、式評価しない)。同じ文字列を持つ Broadcast と ScanMove が同期する |
| `Behavior` | `Class="ScanMove"` のとき使用、自分が到着後に強制遷移する Behavior 名 (生文字列) |
| `TargetBehavior` | `Class="ScanMove"` のとき使用、到着先 Broadcaster が強制遷移する Behavior 名 (生文字列) |
| `BornBehavior` | `Class="Breed"` のとき使用、生成された子マスコットが起動する Behavior 名 (生文字列、式評価しない) |
| `BornX` / `BornY` | `Class="Breed"` のとき使用、親 Anchor からの生成オフセット (式評価可、`${...}` キャッシュ対応)。`BornX` は `LookRight=true` 時に反転 |
| `TransformMascot` | `Class="Transform"` のとき使用、変身先キャラ名 (生文字列、式評価しない)。`-name X` 起動時に X の Action が `TransformMascot="Y"` を含めば Y のテンプレートも自動ロード (1 段だけ) |
| `TransformBehavior` / `TransformBehaviour` | `Class="Transform"` のとき使用、変身先キャラの起動 Behavior 名。英国綴り `TransformBehaviour` もエイリアス |
| `IeOffsetX` / `IeOffsetY` | `Class="WalkWithIE"` / `FallWithIE"` のとき使用 (式評価可)。**LookRight=true 向き** の場合のウィンドウ**左下隅から mascot.Anchor までの差** (shimeji-ee 規約)。Action 開始時にウィンドウを**スナップ**して相対位置を固定する。`X` は `LookRight=false` (左向き) のとき自動反転 (= `BornX` と同じ規則): 右向きなら mascot が左下隅、左向きなら右下隅で抱える。`Y` は反転しない。`IeOffsetX="0" IeOffsetY="-64"` の標準パターン: 右向きで mascot が左下隅の 64px 下、左向きで右下隅の 64px 下。未指定なら現在の相対位置を維持 (スナップなし)。`ThrowIE` も同属性を受けるが shimeji-ee オリジナルでは省略 (前段 Action の位置関係を継承) |
| その他 | `TargetX`, `TargetY`, `InitialVX`, `InitialVY`, `Gravity`, `RegistanceX` / `RegistanceY` (typo そのまま; `Resistance*` もフォールバックで受ける), `Duration`, `Velocity` / `VelocityParam`, `LookRight`, `X` / `Y` (Offset 用) などの**パラメータ属性**。値は式 (`${...}` または `#{...}`) を含みうるので**文字列のまま保持**し、評価時にキャッシュ／毎フレーム評価する |

### Animation / Pose

```xml
<Animation Condition="#{mascot.environment.cursor.y &lt; mascot.environment.screen.height/2}">
  <Pose Image="/shimeji_anzu001.png" ImageAnchor="80,160" Velocity="-2,0" Duration="6" />
</Animation>
```

- 1つの Action は複数 `<Animation>` を持てる。実行時に **Condition を上から評価して最初にマッチしたもの**を採用する。Condition なし = 常にマッチ (フォールバック)
- `Pose` 属性:
  - `Image`: 先頭スラッシュ付き相対パス。`img/[キャラ名]` 配下に解決する
  - `ImageAnchor`: スプライト内のアンカー座標 (X,Y)。マスコット位置との対応点
  - `Velocity`: フレームあたり速度 (X,Y)
  - `Duration`: このポーズを表示するフレーム数
- XML エンティティ: `&lt;`, `&gt;`, `&amp;` が条件式中に現れる。標準パーサーが解決するが、デコード後に再度エスケープしないよう注意

### Action タイプの終了条件 (実行時)

| Type | 終了条件 |
|---|---|
| `Stay` | Animation があれば完走、なければ `Duration` パラメータ経過 (未指定なら 1 tick で終了) |
| `Animate` | 全 Pose 再生完了 (ループなし) |
| `Move` | TargetX / TargetY を**進行方向側で通過**、または `BorderType` 接触を外したとき。距離絶対値ではなく方向通過判定なのは、Target が anchor 付近にランダム抽選された場合に 1 tick 終了するのを防ぐため |
| `Sequence` | 全ステップ完了 (`Loop="true"` なら先頭に戻る) |
| `Select` | 条件を満たす最初の子 Action を実行して終了 |
| `Embedded/Falling` (`Fall`) | 床着地、天井衝突、左右壁衝突 (`HoldOntoWall` を持つキャラなら snap して強制遷移、持たないキャラは `wallRestitution=0.5` で反射継続)、または `Duration` 超過 |
| `Embedded/Jumping` (`Jump`) | `TargetX` 通過 / `TargetY` 通過 / 壁・天井衝突 / 床着地 / 200 tick 安全装置。初速 `VelocityParam` (本家英語版) または `Velocity` (旧日本語版「速度」翻訳結果) を上向きに与え、`Gravity` で減速。`TargetX` 指定があれば滞空時間 `2*v0/g` から水平 `vx` を逆算 |
| `Embedded/Look` (`Turn`) | 即終了。`LookRight` 指定があればその値で固定、未指定なら現在の向きをトグル |
| `Embedded/Offset` (`Move` 別名) | 即終了。`X`/`Y` を Anchor に加算 (`LookRight=true` のとき X を反転) |
| `Embedded/Dragged` | `DragHolding` の間継続。`dragOffset` を維持したままカーソル位置に追従、Animation はループ再生。離されたら即終了 (割り込みで Thrown へ) |
| `Embedded/Broadcast` | アニメ完走 (タイムアウト)、ScanMove 到着、または `BorderType` を外れる。到着時は ScanMove の `TargetBehavior` を強制遷移キューにセット |
| `Embedded/ScanMove` | 該当 Broadcast へ X 方向通過、相手が消滅、または `BorderType` を外れる。到着時は両者の forced 遷移を確定 |
| `Embedded/Breed` | アニメ完走で完了。完了時に `Spawner` へ生成リクエストを投げる (親と同じキャラを `BornBehavior` 起動で `(Anchor + BornX, Anchor + BornY)` に出現させる)。`Spawner=nil` (テスト等) なら no-op |
| `Embedded/Transform` | アニメ完走で完了。完了時に `Spawner.Transform` へ「自分を `TransformMascot` に置き換える」リクエストを投げる。実差し替えは同 tick 末尾の `drain` で「新キャラ spawn → 旧キャラ destroy」順 |
| `Embedded/WalkWithIE` | `stepMove` と同じ終了条件 (`TargetX/Y` 通過 / `BorderType` 外し) に加えて、グラブ中のウィンドウ消失で完了。開始時に activeIE が掴めない (最大化・破棄済み等) なら 1 tick で完了して別 Behavior 抽選にフォール。2 tick 目以降はユーザの手動ウィンドウ移動分を `syncAnchorToGrabbedWindow` で Anchor に逆反映 (ウィンドウから取り残されない) |
| `Embedded/FallWithIE` | `stepFalling` と同じ終了条件 (床着地・速度減衰) に加えてウィンドウ消失で完了。掴めない場合の即時完了は WalkWithIE と同様。手動移動の逆反映も同じ |
| `Embedded/ThrowIE` | **マスコットは投げた瞬間の位置に留まり、ウィンドウだけが独立した物理運動で飛ぶ**。境界は全モニタ WorkArea の連合矩形 (`WorkAreaUnion`) を超えたらクランプ + 速度反転 (Restitution=0.4) + 直交軸 0.7 倍減衰。連続 5 tick 低速 (`|vx|<2 && |vy|<2`) で停止、または 400 tick 安全装置で強制終了。ウィンドウ消失でも完了。XML の `Gravity` / `RegistanceX` は意図的に無視し、`throwRes=0.03` の慣性減衰で穏やかに止まる挙動に統一 |
| `Embedded/Regist`, `Interact`, `BroadcastPosition`, `Sound` | v1 スコープ外。即終了で受け流す |

詳細な状態遷移は [runtime.md](runtime.md) を参照。

## behaviors.xml

### 構造

```xml
<Mascot ...>
  <!-- 任意: キャラ固有定数。現状サポートは maxCount のみ。
       Anzu のように <定数 Name="maxCount" 値="5" /> と書くと、
       JS 変数 maxCount=5 として公開され、Breed の上限制御に使われる。
       未指定時は defaultMaxCount=10 にフォールバック。 -->
  <定数 Name="maxCount" 値="5" />

  <BehaviorList>
    <Behavior Name="..." Frequency="N" [Hidden="true"] [Condition="..."]>
      <NextBehavior Add="true|false">
        <BehaviorReference Name="..." Frequency="N" [Condition="..."] />
        ...
      </NextBehavior>
    </Behavior>

    <!-- 状態 (床/壁/天井) ごとの Condition ブロック -->
    <Condition Condition="#{mascot.environment.floor.isOn(mascot.anchor) ...}">
      <Behavior Name="..." Frequency="N">...</Behavior>
      ...
    </Condition>
  </BehaviorList>
</Mascot>
```

### 必須 Behavior

shimeji-ee の慣習で必ず存在する Behavior。エンジン側から名前指定で呼び出すため、定義されていない場合はエラーにする。

| 名前 | 役割 |
|---|---|
| `ChaseMouse` | マウス追従 (起動時等) |
| `SitAndFaceMouse` | 待機状態のフォールバック |
| `Fall` | 空中にいる時に強制割り込み |
| `Dragged` | ドラッグ開始時に強制割り込み |
| `Thrown` | ドラッグ解放時に強制割り込み |

### 抽選アルゴリズム

1. 現在の物理状態 (床/壁/天井) に合致する `<Condition>` ブロック内 + ルート直下の Behavior を候補として収集
2. 各 Behavior の `Condition` 属性を goja で評価しフィルタ
3. `Frequency` を重みに加重抽選 (Frequency=0 は候補から除外。明示参照されない限り選ばれない)
4. 完了後 `NextBehavior` で次候補を組み立てる:
   - `Add="false"` → `NextBehavior` の中身**だけ**を次の候補集合にする
   - `Add="true"` → `NextBehavior` を通常候補にマージしてから抽選

## 条件式 (`${}` / `#{}`)

XML 中の値 (Action パラメータ・Condition・Animation Condition 等) には JavaScript 式を埋め込める。実行時評価には [`goja`](https://github.com/dop251/goja) を使う。

### 2 種類の埋め込み

| 構文 | 評価タイミング |
|---|---|
| `${...}` | **Action 開始時に一度だけ**評価し `ActionState.CachedParams` にキャッシュ |
| `#{...}` | **毎フレーム**評価 |

両方含む文字列もある。式の境界を正規表現で抜き出して評価し、結果を文字列補間する。

### 公開する変数 (goja Runtime)

[mascot/jsscratch.go](../mascot/jsscratch.go) が `mascot` ルートオブジェクトを構築する。tick ごとに map / closure を再アロケートせず、値フィールドだけ書き換えて使い回す (旧実装は 100 体 × 25 TPS で秒間 2500 回 map 生成して GC を圧迫していた)。

```js
mascot.anchor.{x, y}                        // 仮想デスクトップ絶対座標
mascot.lookRight                            // bool
mascot.totalCount                           // 同キャラの現在個体数 (Breed の maxCount ゲートで使用)
mascot.environment.screen.{left, top, right, bottom, width, height, center, *Border}
mascot.environment.workArea                 // screen のエイリアス
mascot.environment.cursor.{x, y, dx, dy}    // dx/dy は前 tick との差分 (Thrown 初速で使用)
mascot.environment.floor.{value, y, border, isOn(p)}
mascot.environment.ceiling.{value, y, border, isOn(p)}
mascot.environment.wall.{left, right, isOn(p)}
mascot.environment.leftWall.{value, x, isOn(p)}
mascot.environment.rightWall.{value, x, isOn(p)}
mascot.environment.activeIE.{visible, isVisible, left, top, right, bottom, width, height,
                              topBorder, bottomBorder, leftBorder, rightBorder}
```

各 `*Border.isOn(p)` は `p.{x,y}` を受けて接触許容 (`borderTolerance=2px`) で判定する。`activeIE.*Border.isOn` は `activeIE.visible=false` の場合に**常に false** を返すため、ホワイトリスト非合致時に Behavior の Condition が偶発的に通ることはない。

トップレベル変数 (`maxCount`, `FootX`, `FootY`, `footX`, `footY`) も `refreshVM` でセットする (`FootX` / `FootY` は本家がドラッグ時の Pinched Animation で参照する mascot 足元座標、`maxCount` は `<定数>` 値か `defaultMaxCount`)。

shimeji-ee の式中によく現れる呼び出し:

- `mascot.environment.floor.isOn(mascot.anchor)`
- `mascot.environment.cursor.y < mascot.environment.screen.height/2`
- `mascot.environment.activeIE.topBorder.isOn(mascot.anchor)` (実装済み: ホワイトリスト合致ウィンドウの上辺)

### Action パラメータ → VM への展開

`bindActionParamsToVM` が pickAnimation 直前に Action の全パラメータを VM のグローバル変数として set する。本家は Animation の Condition 式で `TargetY < mascot.anchor.y` のように属性名を直接参照する慣習があるため。`paramAliasJP` (例: `TargetX ↔ 目的地X`) に登録された英語名は日本語別名でも公開し、旧日本語版コンテンツの式本文が解決できないケースを防ぐ。

### XML エンティティ

`<` / `>` / `&` は XML エンティティでエスケープされている。`encoding/xml` がデコード後の素の文字列を返すので、その文字列を式パーサ (goja) にそのまま渡せばよい。
