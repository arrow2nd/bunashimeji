package mascot

import "image"

// BroadcastRegistry はプロセス内の全 Mascot 間で Broadcast / ScanMove ハンドシェイクを
// 仲介するレジストリ。
//
// 本家オリジナルは 1 プロセス N キャラなので IPC 不要で同等機能を実現できる。
// 当方も単一プロセス構成のため、この registry を main.go から
// 全 Mascot に共有させるだけで完結する (mutex も不要 = シングルスレッド)。
//
// 利用パターン:
//   - Broadcast Action 開始時: Register(affordance, mascot) で entry を作成、保持
//   - 毎 tick: entry.Position を mascot.Anchor に更新
//   - Action 完了時: Unregister(entry) で除去
//   - ScanMove Action 開始時: FindNearest(affordance, position) で対象 entry を取得
//   - ScanMove 到着時: entry.Arrived = true, entry.TargetBehavior = "..."
//   - 次 tick の Broadcast 側で entry.Arrived を検知し、forcedNext を発動して終了
type BroadcastRegistry struct {
	entries []*BroadcastEntry
}

// BroadcastEntry は 1 件の Broadcast 公示。
// Mascot, Affordance は不変。Position は毎 tick 更新、Arrived/TargetBehavior は ScanMove 到着時に書き込まれる。
type BroadcastEntry struct {
	Mascot     *Mascot
	Affordance string
	Position   image.Point

	// ScanMove 到着で true。Broadcast 側は次 tick で検知して TargetBehavior へ遷移する。
	Arrived        bool
	TargetBehavior string

	// Cancelled は Unregister で true になる。
	// ScanMove 側が「target が消えていないか」を判定するためのフラグ。
	Cancelled bool
}

// NewBroadcastRegistry は空の registry を作る。
func NewBroadcastRegistry() *BroadcastRegistry {
	return &BroadcastRegistry{}
}

// Register は新しい Broadcast 公示を追加して entry を返す。
func (r *BroadcastRegistry) Register(affordance string, m *Mascot, pos image.Point) *BroadcastEntry {
	e := &BroadcastEntry{
		Mascot:     m,
		Affordance: affordance,
		Position:   pos,
	}
	r.entries = append(r.entries, e)
	return e
}

// Unregister は entry を取り除く。多重呼び出し / 既に消えている entry の解除は無害。
// entry.Cancelled = true をセットすることで、まだ entry を保持している ScanMove 側が
// 次 tick で異常終了できる。
func (r *BroadcastRegistry) Unregister(e *BroadcastEntry) {
	if e == nil {
		return
	}
	e.Cancelled = true
	for i, x := range r.entries {
		if x == e {
			r.entries = append(r.entries[:i], r.entries[i+1:]...)
			return
		}
	}
}

// FindNearest は affordance 一致 & 未到着 の entry のうち、from に最も近いものを返す。
// 該当なしなら nil。except に同一 Mascot を渡すと自分自身を除外する (自己ハンドシェイク防止)。
func (r *BroadcastRegistry) FindNearest(affordance string, from image.Point, except *Mascot) *BroadcastEntry {
	var best *BroadcastEntry
	bestDist := -1
	for _, e := range r.entries {
		if e.Affordance != affordance {
			continue
		}
		if e.Arrived || e.Cancelled {
			continue
		}
		if except != nil && e.Mascot == except {
			continue
		}
		d := manhattan(e.Position, from)
		if bestDist < 0 || d < bestDist {
			bestDist = d
			best = e
		}
	}
	return best
}

// Active は現在登録されている entry 一覧を返す (テスト・デバッグ用)。
// 戻り値スライスは内部スライスのコピーなので呼び出し側で改変して良い。
func (r *BroadcastRegistry) Active() []*BroadcastEntry {
	out := make([]*BroadcastEntry, len(r.entries))
	copy(out, r.entries)
	return out
}

func manhattan(a, b image.Point) int {
	return abs(a.X-b.X) + abs(a.Y-b.Y)
}
