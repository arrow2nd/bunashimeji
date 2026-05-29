package mascot

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"image"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// LoadActions は actions.xml をパースする。
// charDir が空でなければ conf/<charDir>/Actions.xml を優先、なければ conf/Actions.xml。
func LoadActions(confRoot, charDir string) (map[string]*Action, error) {
	path, err := resolveConfPath(confRoot, charDir, "actions")
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	data = stripNamespace(data)
	if looksLegacyJapanese(data) {
		data, err = normalizeLegacyJapaneseXML(data)
		if err != nil {
			return nil, fmt.Errorf("normalize %s: %w", path, err)
		}
	}
	// 旧日本語版は <ActionList> を複数持つことがあり、XSD でも
	// `maxOccurs="unbounded"` で許容される。slice で受けて全部マージする。
	var doc struct {
		ActionLists []struct {
			Actions []rawAction `xml:"Action"`
		} `xml:"ActionList"`
	}
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	actions := map[string]*Action{}
	for _, al := range doc.ActionLists {
		for _, ra := range al.Actions {
			a, err := buildAction(ra)
			if err != nil {
				return nil, fmt.Errorf("action %q: %w", ra.Name, err)
			}
			if _, dup := actions[a.Name]; dup {
				return nil, fmt.Errorf("duplicate action name %q in %s", a.Name, path)
			}
			actions[a.Name] = a
		}
	}
	if err := resolveActionRefs(actions); err != nil {
		return nil, err
	}
	return actions, nil
}

// CharacterConstants は <定数> 要素から取り出すキャラ固有定数。
// 該当要素が無ければゼロ値で返り、呼び出し側がデフォルトを充当する。
type CharacterConstants struct {
	MaxCount int // <定数 Name="maxCount" 値="N" />。0 なら未指定。
}

// LoadBehaviors は behaviors.xml をパースする。
// 戻り値の 3 番目は <Mascot> 直下の <定数> 要素から拾った定数群 (Anzu の maxCount=5 等)。
func LoadBehaviors(confRoot, charDir string) ([]*Behavior, []ConditionGroup, CharacterConstants, error) {
	var consts CharacterConstants
	path, err := resolveConfPath(confRoot, charDir, "behaviors")
	if err != nil {
		return nil, nil, consts, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, consts, fmt.Errorf("read %s: %w", path, err)
	}
	data = stripNamespace(data)
	if looksLegacyJapanese(data) {
		data, err = normalizeLegacyJapaneseXML(data)
		if err != nil {
			return nil, nil, consts, fmt.Errorf("normalize %s: %w", path, err)
		}
	}

	// <Mascot> 直下を全要素 (定数 / BehaviorList) で受ける。
	// BehaviorList は最初の 1 つだけ採用 (慣習)。
	var doc struct {
		Items []rawMascotChild `xml:",any"`
	}
	// Mascot ルートの直下要素を受けるため、テンポラリ struct で「Mascot 全体」をパース。
	var root struct {
		XMLName xml.Name
		Items   []rawMascotChild `xml:",any"`
	}
	if err := xml.Unmarshal(data, &root); err != nil {
		return nil, nil, consts, fmt.Errorf("parse %s: %w", path, err)
	}
	doc.Items = root.Items

	var rootBehaviors []*Behavior
	var groups []ConditionGroup
	for _, item := range doc.Items {
		switch item.XMLName.Local {
		case "定数":
			// <定数 Name="X" 値="N" /> — UTF-8 タグ。Name=maxCount のみ拾う。
			if item.ConstName == "maxCount" {
				if n, err := strconv.Atoi(strings.TrimSpace(item.ConstValue)); err == nil && n > 0 {
					consts.MaxCount = n
				}
			}
		case "BehaviorList":
			for _, b := range item.BehaviorListItems {
				switch b.XMLName.Local {
				case "Behavior":
					built, err := buildBehavior(b.toBehavior())
					if err != nil {
						return nil, nil, consts, err
					}
					rootBehaviors = append(rootBehaviors, built)
				case "Condition":
					cond, err := NewEvaluator(b.Condition)
					if err != nil {
						return nil, nil, consts, fmt.Errorf("condition group %q: %w", b.Condition, err)
					}
					grp := ConditionGroup{Condition: cond}
					for _, child := range b.Children {
						if child.XMLName.Local != "Behavior" {
							continue
						}
						built, err := buildBehavior(child.toBehavior())
						if err != nil {
							return nil, nil, consts, err
						}
						grp.Behaviors = append(grp.Behaviors, built)
					}
					groups = append(groups, grp)
				}
			}
		}
	}
	return rootBehaviors, groups, consts, nil
}

// rawMascotChild は <Mascot> 直下のあらゆる要素を受けるためのカチット構造体。
// XMLName で型を識別し、`定数` / `BehaviorList` 等それぞれに必要な属性を持つ。
type rawMascotChild struct {
	XMLName xml.Name

	// <定数 Name="..." 値="..." /> 用
	ConstName  string `xml:"Name,attr"`
	ConstValue string `xml:"値,attr"`

	// <BehaviorList> の子要素 (Behavior / Condition) を保持
	BehaviorListItems []rawBehaviorListItem `xml:",any"`
}

// CharacterDirs は img/ 配下のキャラディレクトリ一覧を返す (`unused` は除外)。
func CharacterDirs(imgRoot string) ([]string, error) {
	entries, err := os.ReadDir(imgRoot)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", imgRoot, err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if strings.EqualFold(e.Name(), "unused") {
			continue
		}
		names = append(names, e.Name())
	}
	return names, nil
}

func stripNamespace(data []byte) []byte {
	for _, ns := range [][]byte{
		[]byte(` xmlns="http://www.group-finity.com/Mascot"`),
		[]byte(` xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"`),
	} {
		data = bytes.ReplaceAll(data, ns, nil)
	}
	// xsi:schemaLocation 属性は値を含む可変長なので部分一致で削る
	for {
		i := bytes.Index(data, []byte(" xsi:schemaLocation=\""))
		if i < 0 {
			break
		}
		end := bytes.IndexByte(data[i+len(" xsi:schemaLocation=\""):], '"')
		if end < 0 {
			break
		}
		data = append(data[:i], data[i+len(" xsi:schemaLocation=\"")+end+1:]...)
	}
	return data
}

// resolveConfPath は actions/behaviors の XML を優先順位 (キャラ固有 → ルート) と
// case-insensitive ファイル名で探す。
func resolveConfPath(confRoot, charDir, kind string) (string, error) {
	// 候補: actions.xml / Actions.xml / Action.xml (単数) / 動作.xml (旧日本語版)
	wants := []string{kind + ".xml"}
	if kind == "behaviors" {
		wants = append(wants, "behavior.xml", "行動.xml")
	}
	if kind == "actions" {
		wants = append(wants, "action.xml", "動作.xml")
	}
	dirs := []string{}
	if charDir != "" {
		// case-insensitive キャラディレクトリ解決
		if d, err := caseInsensitiveDir(confRoot, charDir); err == nil {
			dirs = append(dirs, d)
		}
	}
	dirs = append(dirs, confRoot)
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			for _, w := range wants {
				if strings.EqualFold(e.Name(), w) {
					return filepath.Join(d, e.Name()), nil
				}
			}
		}
	}
	return "", fmt.Errorf("%s.xml not found under %s (char=%q)", kind, confRoot, charDir)
}

func caseInsensitiveDir(root, name string) (string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.IsDir() && strings.EqualFold(e.Name(), name) {
			return filepath.Join(root, e.Name()), nil
		}
	}
	return "", fmt.Errorf("dir %q not under %s", name, root)
}

// ----------- Action 構築 -----------

type rawAction struct {
	XMLName    xml.Name   `xml:"Action"`
	Name       string     `xml:"Name,attr"`
	Type       string     `xml:"Type,attr"`
	BorderType string     `xml:"BorderType,attr"`
	Class      string     `xml:"Class,attr"`
	Loop       string     `xml:"Loop,attr"`
	Attrs      []xml.Attr `xml:",any,attr"`

	// Children に Action / ActionReference / Animation を出現順で受ける。
	// Sequence/Select は XML 上の順序が実行順なので、別フィールドに分けず順序保持する。
	Children []rawActionChild `xml:",any"`
}

type rawActionChild struct {
	XMLName xml.Name
	// Animation 用
	AnimCondition string    `xml:"Condition,attr"`
	Poses         []rawPose `xml:"Pose"`
	// Action / ActionReference 共通の属性: Name, 他は Attrs
	Name  string     `xml:"Name,attr"`
	Attrs []xml.Attr `xml:",any,attr"`
	// Action インライン子の場合は再帰
	Type       string             `xml:"Type,attr"`
	BorderType string             `xml:"BorderType,attr"`
	Class      string             `xml:"Class,attr"`
	Loop       string             `xml:"Loop,attr"`
	Sub        []rawActionChild   `xml:",any"`
}

type rawPose struct {
	Image       string `xml:"Image,attr"`
	ImageAnchor string `xml:"ImageAnchor,attr"`
	Velocity    string `xml:"Velocity,attr"`
	Duration    string `xml:"Duration,attr"`
}

func buildAction(ra rawAction) (*Action, error) {
	return buildActionFromChild(rawActionChild{
		XMLName:    xml.Name{Local: "Action"},
		Name:       ra.Name,
		Attrs:      ra.Attrs,
		Type:       ra.Type,
		BorderType: ra.BorderType,
		Class:      ra.Class,
		Loop:       ra.Loop,
		Sub:        ra.Children,
	})
}

// buildActionFromChild は Action 要素 (top-level または Sequence/Select の子) を構築する。
// Sub に並ぶ Animation / Action / ActionReference の出現順を保ち、
// ActionReference は RefName 付きプレースホルダとして Children に挿入する (resolveActionRefs で実体化)。
func buildActionFromChild(c rawActionChild) (*Action, error) {
	a := &Action{
		Name:       c.Name,
		Type:       c.Type,
		BorderType: c.BorderType,
		Class:      classTail(c.Class),
		Loop:       strings.EqualFold(c.Loop, "true"),
		Params:     map[string]*Evaluator{},
	}
	for _, attr := range c.Attrs {
		switch attr.Name.Local {
		case "Name", "Type", "BorderType", "Class", "Loop":
			continue
		case "Affordance":
			// Broadcast / ScanMove のハンドシェイクキー。式評価しない。
			a.Affordance = attr.Value
			continue
		case "Behavior":
			// ScanMove: 自分が到着後に遷移する Behavior 名。式評価しない。
			a.BehaviorAttr = attr.Value
			continue
		case "TargetBehavior":
			// ScanMove: Broadcaster (到着先) が遷移する Behavior 名。式評価しない。
			a.TargetBehaviorAttr = attr.Value
			continue
		case "BornBehavior":
			// Breed: 生成された子 Mascot が起動する Behavior 名。式評価しない。
			a.BornBehavior = attr.Value
			continue
		case "TransformMascot":
			// Transform: 変身先キャラ名。式評価しない。
			a.TransformMascot = attr.Value
			continue
		case "TransformBehavior", "TransformBehaviour":
			// Transform: 変身先キャラの起動 Behavior 名。英国綴り (Behaviour) もエイリアス。
			a.TransformBehavior = attr.Value
			continue
		}
		ev, err := NewEvaluator(attr.Value)
		if err != nil {
			return nil, err
		}
		a.Params[attr.Name.Local] = ev
	}
	for _, sub := range c.Sub {
		switch sub.XMLName.Local {
		case "Animation":
			anim, err := buildAnimation(sub)
			if err != nil {
				return nil, err
			}
			a.Animations = append(a.Animations, anim)
		case "Action":
			child, err := buildActionFromChild(sub)
			if err != nil {
				return nil, err
			}
			a.Children = append(a.Children, child)
		case "ActionReference":
			placeholder, err := buildRefPlaceholder(sub)
			if err != nil {
				return nil, err
			}
			a.Children = append(a.Children, placeholder)
		}
	}
	return a, nil
}

func buildAnimation(c rawActionChild) (Animation, error) {
	cond, err := NewEvaluator(c.AnimCondition)
	if err != nil {
		return Animation{}, err
	}
	anim := Animation{Condition: cond}
	for _, p := range c.Poses {
		pose, err := buildPose(p)
		if err != nil {
			return Animation{}, err
		}
		anim.Poses = append(anim.Poses, pose)
	}
	return anim, nil
}

func buildRefPlaceholder(c rawActionChild) (*Action, error) {
	a := &Action{
		RefName: c.Name,
		Params:  map[string]*Evaluator{},
	}
	for _, attr := range c.Attrs {
		switch attr.Name.Local {
		case "Name":
			continue
		case "Affordance":
			a.Affordance = attr.Value
			continue
		case "Behavior":
			a.BehaviorAttr = attr.Value
			continue
		case "TargetBehavior":
			a.TargetBehaviorAttr = attr.Value
			continue
		case "BornBehavior":
			a.BornBehavior = attr.Value
			continue
		case "TransformMascot":
			a.TransformMascot = attr.Value
			continue
		case "TransformBehavior", "TransformBehaviour":
			a.TransformBehavior = attr.Value
			continue
		}
		ev, err := NewEvaluator(attr.Value)
		if err != nil {
			return nil, err
		}
		a.Params[attr.Name.Local] = ev
	}
	return a, nil
}

func buildPose(p rawPose) (Pose, error) {
	anchor, err := parsePoint(p.ImageAnchor)
	if err != nil {
		return Pose{}, fmt.Errorf("ImageAnchor %q: %w", p.ImageAnchor, err)
	}
	vel, err := parsePoint(p.Velocity)
	if err != nil {
		return Pose{}, fmt.Errorf("Velocity %q: %w", p.Velocity, err)
	}
	dur := 1
	if p.Duration != "" {
		n, err := strconv.Atoi(strings.TrimSpace(p.Duration))
		if err != nil {
			return Pose{}, fmt.Errorf("Duration %q: %w", p.Duration, err)
		}
		dur = n
	}
	return Pose{
		Image:       p.Image,
		ImageAnchor: anchor,
		Velocity:    vel,
		Duration:    dur,
	}, nil
}

func parsePoint(s string) (image.Point, error) {
	if s == "" {
		return image.Point{}, nil
	}
	parts := strings.Split(s, ",")
	if len(parts) != 2 {
		return image.Point{}, fmt.Errorf("expected x,y got %q", s)
	}
	x, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return image.Point{}, err
	}
	y, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return image.Point{}, err
	}
	return image.Point{X: x, Y: y}, nil
}

func classTail(fqcn string) string {
	if fqcn == "" {
		return ""
	}
	i := strings.LastIndex(fqcn, ".")
	if i < 0 {
		return fqcn
	}
	return fqcn[i+1:]
}

// resolveActionRefs は ActionReference を Children に展開する。
// 同名 Action を辿るので循環は深さ制限で防ぐ。
func resolveActionRefs(actions map[string]*Action) error {
	for _, a := range actions {
		if err := resolveOne(a, actions, map[string]bool{}, 0); err != nil {
			return err
		}
	}
	return nil
}

func resolveOne(a *Action, all map[string]*Action, visiting map[string]bool, depth int) error {
	if depth > 32 {
		return fmt.Errorf("action %q: depth limit exceeded (cycle?)", a.Name)
	}
	if a.Name != "" {
		if visiting[a.Name] {
			return nil // 循環は無視 (実害なし)
		}
		visiting[a.Name] = true
		defer delete(visiting, a.Name)
	}
	// プレースホルダを target Action のシャロークローンで実体化する。
	// 出現順 (Children) を維持したまま要素を入れ替える。
	for i, ch := range a.Children {
		if ch.RefName == "" {
			continue
		}
		target, ok := all[ch.RefName]
		if !ok {
			return fmt.Errorf("action %q: unknown reference %q", a.Name, ch.RefName)
		}
		if err := resolveOne(target, all, visiting, depth+1); err != nil {
			return err
		}
		clone := *target
		// 参照側パラメータは target のパラメータを上書きする (target を変更しないようコピー)
		clone.Params = map[string]*Evaluator{}
		maps.Copy(clone.Params, target.Params)
		maps.Copy(clone.Params, ch.Params)
		// Affordance / Behavior / TargetBehavior / BornBehavior 属性も Reference 側があれば上書き
		if ch.Affordance != "" {
			clone.Affordance = ch.Affordance
		}
		if ch.BehaviorAttr != "" {
			clone.BehaviorAttr = ch.BehaviorAttr
		}
		if ch.TargetBehaviorAttr != "" {
			clone.TargetBehaviorAttr = ch.TargetBehaviorAttr
		}
		if ch.BornBehavior != "" {
			clone.BornBehavior = ch.BornBehavior
		}
		if ch.TransformMascot != "" {
			clone.TransformMascot = ch.TransformMascot
		}
		if ch.TransformBehavior != "" {
			clone.TransformBehavior = ch.TransformBehavior
		}
		a.Children[i] = &clone
	}
	for _, ch := range a.Children {
		if err := resolveOne(ch, all, visiting, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// ----------- Behavior 構築 -----------

type rawBehaviorListItem struct {
	XMLName   xml.Name
	Name      string                `xml:"Name,attr"`
	Frequency string                `xml:"Frequency,attr"`
	Hidden    string                `xml:"Hidden,attr"`
	Condition string                `xml:"Condition,attr"`
	Next      []rawNextBehavior     `xml:"NextBehavior"`
	Children  []rawBehaviorListItem `xml:",any"`
}

type rawNextBehavior struct {
	Add  string             `xml:"Add,attr"`
	Refs []rawBehaviorRefEl `xml:"BehaviorReference"`
}

type rawBehaviorRefEl struct {
	Name      string `xml:"Name,attr"`
	Frequency string `xml:"Frequency,attr"`
	Condition string `xml:"Condition,attr"`
}

type rawBehavior struct {
	Name      string
	Frequency string
	Hidden    string
	Condition string
	Next      []rawNextBehavior
}

func (it rawBehaviorListItem) toBehavior() rawBehavior {
	return rawBehavior{
		Name:      it.Name,
		Frequency: it.Frequency,
		Hidden:    it.Hidden,
		Condition: it.Condition,
		Next:      it.Next,
	}
}

func buildBehavior(rb rawBehavior) (*Behavior, error) {
	freq, _ := strconv.Atoi(strings.TrimSpace(rb.Frequency))
	cond, err := NewEvaluator(rb.Condition)
	if err != nil {
		return nil, fmt.Errorf("behavior %q condition %q: %w", rb.Name, rb.Condition, err)
	}
	b := &Behavior{
		Name:      rb.Name,
		Frequency: freq,
		Hidden:    strings.EqualFold(rb.Hidden, "true"),
		Condition: cond,
	}
	for _, n := range rb.Next {
		nb := NextBehavior{Add: strings.EqualFold(n.Add, "true")}
		for _, r := range n.Refs {
			rfreq, _ := strconv.Atoi(strings.TrimSpace(r.Frequency))
			rcond, err := NewEvaluator(r.Condition)
			if err != nil {
				return nil, fmt.Errorf("behavior %q ref %q: %w", rb.Name, r.Name, err)
			}
			nb.References = append(nb.References, BehaviorRef{
				Name:      r.Name,
				Frequency: rfreq,
				Condition: rcond,
			})
		}
		b.NextBehaviors = append(b.NextBehaviors, nb)
	}
	return b, nil
}
