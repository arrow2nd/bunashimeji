package mascot

import "log"

// markAirHandlers は各 Behavior の HandlesAir を計算してセットする。
// Action ツリーを再帰的に辿り、Embedded Class が Fall/Jump 系を含むなら true。
// 必須 Behavior の Fall/Thrown も明示的に true にする。
func (m *Mascot) markAirHandlers() {
	all := append([]*Behavior(nil), m.Behaviors...)
	for _, g := range m.ConditionGroups {
		all = append(all, g.Behaviors...)
	}
	for _, b := range all {
		if behaviorMatchesRole(b, "Fall") || behaviorMatchesRole(b, "Thrown") {
			b.HandlesAir = true
			continue
		}
		a, ok := m.Actions[b.Name]
		if !ok {
			continue
		}
		b.HandlesAir = actionContainsAir(a, map[*Action]bool{})
	}
}

func actionContainsAir(a *Action, visited map[*Action]bool) bool {
	if a == nil || visited[a] {
		return false
	}
	visited[a] = true
	if a.Type == "Embedded" {
		switch a.Class {
		case "Fall", "Falling", "Jump", "Jumping":
			return true
		}
	}
	for _, ch := range a.Children {
		if actionContainsAir(ch, visited) {
			return true
		}
	}
	return false
}

// checkInterrupt は割り込み Behavior (Fall/Dragged/Thrown/forcedNext) を返す。
// 該当しなければ nil。
//
// 優先順位:
//  1. Drag 開始 / 解放 (forcedNext は捨てる: 掴まれたら会話遷移は無効化)
//  2. forcedNext (ScanMove 到着時にセットされる強制遷移)
//  3. 空中 → Fall
func (m *Mascot) checkInterrupt() *Behavior {
	// notifyAffordanceInterrupt は進行中の Broadcast/ScanMove Action がある時に
	// 「何で中断されたか」を OnAffordance に通知する。これがないと Drag/Fall 等で
	// アフォーダンス Action が破棄されても終了ログが出ず、観測者から見て
	// 「結末不明なハンドシェイク」になってしまう。
	notifyAffordanceInterrupt := func(reason string) {
		if m.CurrentAction == nil || m.CurrentAction.Action == nil || m.OnAffordance == nil {
			return
		}
		a := m.CurrentAction.Action
		switch a.Class {
		case "Broadcast":
			m.OnAffordance("broadcast-interrupted", a.Affordance, reason)
		case "ScanMove":
			m.OnAffordance("scanmove-interrupted", a.Affordance, reason)
		}
	}

	switch m.Drag {
	case DragStarted:
		// ドラッグ開始 → Dragged Behavior に遷移、状態は Holding に進める
		m.Drag = DragHolding
		m.forcedNext = nil // 掴まれたら会話ハンドシェイクは破棄
		m.clearGrab()      // WalkWithIE 等で掴んでいた外部ウィンドウは解放
		if b, ok := m.findBehaviorByRole("Dragged"); ok {
			notifyAffordanceInterrupt("Dragged")
			return b
		}
	case DragReleased:
		// ドラッグ解放 → Thrown 1 回、状態を Done に戻す
		m.Drag = DragNone
		m.forcedNext = nil
		m.clearGrab()
		// 投げ方向に向きを合わせる。Thrown の InitialVX は cursor.dx を使うので
		// 同じ値の符号で LookRight を決めれば飛んでいく方向に常に正対する。
		// dx==0 (静止リリース) のときは現在の向きを維持。
		if dx := m.env.CursorDelta.X; dx != 0 {
			m.LookRight = dx > 0
		}
		if b, ok := m.findBehaviorByRole("Thrown"); ok {
			notifyAffordanceInterrupt("Thrown")
			return b
		}
	}
	// forcedNext (ScanMove 到着 → 双方の Behavior 強制遷移) を優先。
	// これは ScanMove 自身が成立して投げているフラグなので interrupted 扱いしない
	// (既に scanmove-arrived / broadcast-arrived がログ済)。
	if m.forcedNext != nil {
		next := m.forcedNext
		m.forcedNext = nil
		if b, ok := m.findBehaviorByName(next.Name); ok {
			return b
		}
	}
	// 空中判定: 床にも壁にも天井にも接していない
	if m.Drag == DragNone &&
		!m.env.IsOnFloor(m.Anchor) &&
		!m.env.IsOnCeiling(m.Anchor) &&
		!m.env.IsOnWall(m.Anchor) {
		// 現 Behavior が空中遷移を内包している (Falling/Jumping を Action に含む) 場合は
		// 意図的な投げ上げ等を中断しないよう Fall 割り込みをスキップ。
		if m.CurrentBehavior != nil && m.CurrentBehavior.HandlesAir {
			return nil
		}
		if b, ok := m.findBehaviorByRole("Fall"); ok {
			notifyAffordanceInterrupt("Fall")
			return b
		}
	}
	return nil
}

// collectNextRefs は完了した Behavior の NextBehavior 群を解決して候補リストに変える。
// 戻り値の replace=true なら通常抽選を行わず candidates のみで次の Behavior を決定する。
// Add="false" のグループがあれば、そのグループ単体で確定させる (replace=true)。
// Add="true" のグループは通常抽選とマージする (replace=false)。
func (m *Mascot) collectNextRefs(b *Behavior) (candidates []BehaviorRef, replace bool) {
	var add []BehaviorRef
	for _, nb := range b.NextBehaviors {
		passes := []BehaviorRef{}
		for _, r := range nb.References {
			ok, err := r.Condition.EvalBool(m.vm, nil)
			if err != nil {
				log.Printf("warning: behavior %q ref %q condition: %v", b.Name, r.Name, err)
				continue
			}
			if !ok {
				continue
			}
			passes = append(passes, r)
		}
		if len(passes) == 0 {
			continue
		}
		if !nb.Add {
			// 置換: この集合だけで次回候補を決める
			return passes, true
		}
		add = append(add, passes...)
	}
	return add, false
}

// pickNextBehavior は次に遷移する Behavior を Frequency 加重ルーレット選択で決める。
//
// 本家オリジナルの仕様:
//   - Behavior 抽選 = 重み付きルーレット (Frequency をそのまま重みに使う)
//   - 候補集合 = ルート + 該当する全 <Condition> グループ + NextBehavior 由来 (Add="true" なら通常候補とマージ、Add="false" なら置換)
//   - Frequency=0 は通常抽選では除外。NextBehavior で名前指定された場合のみ採用 (重みは BehaviorRef.Frequency か、それも 0 なら最小値 1)
func (m *Mascot) pickNextBehavior() *Behavior {
	var cands []behaviorCand

	if m.pendingReplace {
		// Add="false" 由来: pendingNext のみで決定 (置換)
		cands = append(cands, m.refsToCands(m.pendingNext)...)
	} else {
		// 通常抽選: ルート + 該当する Condition グループ
		cands = append(cands, m.behaviorsToCands(m.Behaviors)...)
		for _, g := range m.ConditionGroups {
			ok, err := g.Condition.EvalBool(m.vm, nil)
			if err != nil {
				log.Printf("warning: condition group: %v", err)
				continue
			}
			if !ok {
				continue
			}
			cands = append(cands, m.behaviorsToCands(g.Behaviors)...)
		}
		// NextBehavior Add="true" 由来をマージ
		if len(m.pendingNext) > 0 {
			cands = append(cands, m.refsToCands(m.pendingNext)...)
		}
	}

	if len(cands) == 0 {
		return nil
	}
	weights := make([]int, len(cands))
	for i, c := range cands {
		weights[i] = c.w
	}
	idx := weightedPick(weights)
	if idx < 0 {
		return nil
	}
	m.pendingNext = nil
	m.pendingReplace = false
	return cands[idx].b
}

type behaviorCand struct {
	b *Behavior
	w int
}

// CandidateInfo はデバッグ HUD で表示する次回抽選候補の 1 件。
type CandidateInfo struct {
	Name   string
	Weight int
	Source string // "root" / "cond" / "next"
}

// CurrentCandidates は次に advanceBehavior が引くであろう候補集合を返す (デバッグ用)。
// pickNextBehavior と同じ集計ロジックを使う。
func (m *Mascot) CurrentCandidates() []CandidateInfo {
	var out []CandidateInfo
	add := func(cs []behaviorCand, src string) {
		for _, c := range cs {
			out = append(out, CandidateInfo{Name: c.b.Name, Weight: c.w, Source: src})
		}
	}
	if m.pendingReplace {
		add(m.refsToCands(m.pendingNext), "next")
		return out
	}
	add(m.behaviorsToCands(m.Behaviors), "root")
	for _, g := range m.ConditionGroups {
		ok, err := g.Condition.EvalBool(m.vm, nil)
		if err != nil || !ok {
			continue
		}
		add(m.behaviorsToCands(g.Behaviors), "cond")
	}
	if len(m.pendingNext) > 0 {
		add(m.refsToCands(m.pendingNext), "next")
	}
	return out
}

// behaviorsToCands は通常抽選用 (ルート or Condition グループ内) の候補リストを作る。
// Frequency=0 と Condition false は除外。
func (m *Mascot) behaviorsToCands(behaviors []*Behavior) []behaviorCand {
	var out []behaviorCand
	for _, b := range behaviors {
		if b.Frequency <= 0 {
			continue
		}
		ok, err := b.Condition.EvalBool(m.vm, nil)
		if err != nil {
			log.Printf("warning: behavior %q condition: %v", b.Name, err)
			continue
		}
		if !ok {
			continue
		}
		out = append(out, behaviorCand{b: b, w: b.Frequency})
	}
	return out
}

// refsToCands は NextBehavior 由来の参照を候補化する。
// 明示参照なので Frequency=0 でも採用可。
func (m *Mascot) refsToCands(refs []BehaviorRef) []behaviorCand {
	var out []behaviorCand
	for _, r := range refs {
		b, ok := m.findBehaviorByName(r.Name)
		if !ok {
			continue
		}
		w := r.Frequency
		if w <= 0 {
			w = b.Frequency
		}
		if w <= 0 {
			w = 1
		}
		out = append(out, behaviorCand{b: b, w: w})
	}
	return out
}
