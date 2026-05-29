package mascot

import "image"

type Action struct {
	Name       string
	Type       string
	BorderType string
	Class      string
	Loop       bool
	Animations []Animation
	Children   []*Action // インライン Action と解決済 ActionReference を出現順で保持
	Params     map[string]*Evaluator

	// RefName が非空の場合、Children の中で「未解決の ActionReference プレースホルダ」を表す。
	// resolveActionRefs がこれを target Action のシャロークローン (Params マージ済) で置き換える。
	RefName string

	// Broadcast / ScanMove ハンドシェイク用の生文字列属性。
	// 本家のスキーマ上、これらは式評価せず文字列キーとして使うため Params とは独立。
	// 該当 Class 以外では使用されない (Broadcast: Affordance のみ / ScanMove: 全 3 つ)。
	Affordance         string
	BehaviorAttr       string // ScanMove: 自分の次 Behavior 名
	TargetBehaviorAttr string // ScanMove: 到着先 (Broadcaster) の次 Behavior 名

	// Breed (Class="Breed") の生成子 Mascot が起動する Behavior 名。生文字列。
	// BornX / BornY は式評価が必要なので Params 経由 (stepBreed が EvalInt する)。
	BornBehavior string

	// Transform (Class="Transform") 用の生文字列属性。
	// TransformMascot: 変身先キャラ名 (例: "Nagi")。stepTransform が Spawner.Transform へ渡す。
	// TransformBehavior: 変身先キャラの起動 Behavior 名。"TransformBehaviour" (英) もエイリアス。
	TransformMascot   string
	TransformBehavior string
}

type Animation struct {
	Condition *Evaluator
	Poses     []Pose
}

type Pose struct {
	Image       string
	ImageAnchor image.Point
	Velocity    image.Point
	Duration    int
}

type Behavior struct {
	Name          string
	Frequency     int
	Hidden        bool
	Condition     *Evaluator
	NextBehaviors []NextBehavior

	// HandlesAir=true の Behavior は内部で意図的に空中になる (Falling/Jumping を含む)。
	// checkInterrupt の Fall 強制割り込みをこれらの Behavior 中はスキップする。
	HandlesAir bool
}

type NextBehavior struct {
	Add        bool
	References []BehaviorRef
}

type BehaviorRef struct {
	Name      string
	Frequency int
	Condition *Evaluator
}

type ConditionGroup struct {
	Condition *Evaluator
	Behaviors []*Behavior
}

type ActionState struct {
	Action       *Action
	PoseIndex    int
	PoseTick     int
	SequenceStep int
	ChildState   *ActionState
	StartTick    int
	CachedParams map[string]any
	StartAnchor  image.Point
}
