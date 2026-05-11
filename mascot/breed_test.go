package mascot

import (
	"path/filepath"
	"testing"
)

// mockSpawner は stepBreed の Spawn 呼び出しを記録する。
type mockSpawner struct {
	requests []SpawnRequest
}

func (s *mockSpawner) Spawn(req SpawnRequest) {
	s.requests = append(s.requests, req)
}

// TestStepBreedQueuesSpawn は Anzu の Divide1 Action を直接駆動して、
// アニメーション完走時に Spawner.Spawn が想定通りに呼ばれることを検証する。
//
// Divide1 は BornX="-16" BornY="0" BornBehavior="Divided" を持つ。
// LookRight=true の Anzu インスタンスでは BornX が反転されるので
// req.Anchor.X = mascot.Anchor.X + 16 となるはず。
func TestStepBreedQueuesSpawn(t *testing.T) {
	root := filepath.Join("..")
	confDir := filepath.Join(root, "conf")
	imgDir := filepath.Join(root, "img")

	tpl, err := LoadCharacterTemplate(confDir, imgDir, "Anzu")
	if err != nil {
		t.Skipf("anzu template not available: %v", err)
	}

	sp := &mockSpawner{}
	m := tpl.NewInstance(nil, sp, nil, InstanceOpts{})
	// LookRight が NewInstance のデフォルト位置決めで true になっていることを確認しておく
	if !m.LookRight {
		t.Fatalf("expected default LookRight=true, got false")
	}
	parentAnchor := m.Anchor

	a, ok := tpl.Actions["Divide1"]
	if !ok {
		t.Skip("anzu Divide1 action not found")
	}
	state := newActionState(a, m)

	// Divide1 Animation の Pose Duration 合計は約 34 frames。安全マージンで 200 tick。
	var done bool
	for i := 0; i < 200; i++ {
		done = StepAction(state, m, m.env)
		if done {
			break
		}
	}
	if !done {
		t.Fatal("Divide1 did not complete within 200 ticks")
	}

	if len(sp.requests) != 1 {
		t.Fatalf("expected exactly 1 spawn request, got %d", len(sp.requests))
	}
	req := sp.requests[0]
	if req.ParentName != "Anzu" {
		t.Errorf("ParentName: got %q, want Anzu", req.ParentName)
	}
	if req.InitialBehavior != "Divided" {
		t.Errorf("InitialBehavior: got %q, want Divided", req.InitialBehavior)
	}
	// LookRight=true → BornX=-16 が +16 にミラー
	wantX := parentAnchor.X + 16
	if req.Anchor.X != wantX {
		t.Errorf("Anchor.X: got %d, want %d (parent %d + mirrored BornX 16)", req.Anchor.X, wantX, parentAnchor.X)
	}
	if req.Anchor.Y != parentAnchor.Y {
		t.Errorf("Anchor.Y: got %d, want %d (BornY=0)", req.Anchor.Y, parentAnchor.Y)
	}
	if !req.LookRight {
		t.Errorf("LookRight: got false, want true (inherited from parent)")
	}
}

// TestStepBreedNoSpawner は Spawner=nil でも stepBreed が無害に終わることを確認する。
func TestStepBreedNoSpawner(t *testing.T) {
	root := filepath.Join("..")
	confDir := filepath.Join(root, "conf")
	imgDir := filepath.Join(root, "img")

	tpl, err := LoadCharacterTemplate(confDir, imgDir, "Anzu")
	if err != nil {
		t.Skipf("anzu template not available: %v", err)
	}
	m := tpl.NewInstance(nil, nil, nil, InstanceOpts{}) // spawner=nil

	a, ok := tpl.Actions["Divide1"]
	if !ok {
		t.Skip("anzu Divide1 action not found")
	}
	state := newActionState(a, m)
	for i := 0; i < 200; i++ {
		if StepAction(state, m, m.env) {
			return // panic せず完走 = 期待通り
		}
	}
	t.Fatal("Divide1 did not complete with nil spawner")
}

// TestNewInstanceMaxCountFromConstants は <定数 maxCount> がインスタンスへ
// 反映されていることを確認する。
func TestNewInstanceMaxCountFromConstants(t *testing.T) {
	root := filepath.Join("..")
	confDir := filepath.Join(root, "conf")
	imgDir := filepath.Join(root, "img")

	tpl, err := LoadCharacterTemplate(confDir, imgDir, "Anzu")
	if err != nil {
		t.Skipf("anzu template not available: %v", err)
	}
	m := tpl.NewInstance(nil, nil, nil, InstanceOpts{})
	if m.maxCount != 5 {
		t.Errorf("Anzu maxCount: got %d, want 5 (from <定数>)", m.maxCount)
	}
}
