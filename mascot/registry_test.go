package mascot

import (
	"image"
	"testing"
)

func TestRegistryRegisterAndFindNearest(t *testing.T) {
	r := NewBroadcastRegistry()
	mA := &Mascot{Name: "A"}
	mB := &Mascot{Name: "B"}
	mC := &Mascot{Name: "C"}

	// 3 件登録: A は別 affordance、B/C は同じ affordance で位置違い
	r.Register("other", mA, image.Pt(0, 0))
	eB := r.Register("greet", mB, image.Pt(100, 0))
	eC := r.Register("greet", mC, image.Pt(500, 0))

	// pos=(120, 0) から FindNearest("greet") → B が近い
	got := r.FindNearest("greet", image.Pt(120, 0), nil)
	if got != eB {
		t.Fatalf("FindNearest near B: got %+v, want %+v", got, eB)
	}

	// pos=(450, 0) → C が近い
	got = r.FindNearest("greet", image.Pt(450, 0), nil)
	if got != eC {
		t.Fatalf("FindNearest near C: got %+v, want %+v", got, eC)
	}

	// affordance 不一致 → nil
	if got := r.FindNearest("missing", image.Pt(0, 0), nil); got != nil {
		t.Fatalf("FindNearest missing: got %+v, want nil", got)
	}

	// except (自己除外): mB を except 指定すると B はスキップ → C が選ばれる
	got = r.FindNearest("greet", image.Pt(120, 0), mB)
	if got != eC {
		t.Fatalf("FindNearest except B: got %+v, want %+v", got, eC)
	}
}

func TestRegistryUnregister(t *testing.T) {
	r := NewBroadcastRegistry()
	m := &Mascot{Name: "X"}
	e := r.Register("greet", m, image.Pt(0, 0))

	r.Unregister(e)
	if !e.Cancelled {
		t.Fatal("Cancelled should be true after Unregister")
	}
	if got := r.FindNearest("greet", image.Pt(0, 0), nil); got != nil {
		t.Fatalf("FindNearest after Unregister: got %+v, want nil", got)
	}
	if got := r.Active(); len(got) != 0 {
		t.Fatalf("Active after Unregister: len=%d, want 0", len(got))
	}

	// 多重 Unregister は無害
	r.Unregister(e)
	r.Unregister(nil)
}

func TestRegistryFindNearestSkipsArrived(t *testing.T) {
	r := NewBroadcastRegistry()
	m1 := &Mascot{Name: "1"}
	m2 := &Mascot{Name: "2"}
	e1 := r.Register("greet", m1, image.Pt(0, 0))
	e2 := r.Register("greet", m2, image.Pt(100, 0))

	// e1 を到着済みにマーク → FindNearest は e2 を返す
	e1.Arrived = true
	got := r.FindNearest("greet", image.Pt(0, 0), nil)
	if got != e2 {
		t.Fatalf("FindNearest skipping Arrived: got %+v, want %+v", got, e2)
	}
}
