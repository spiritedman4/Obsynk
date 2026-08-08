package syncengine

import "testing"

func TestPlan_FiltersDecisionNone(t *testing.T) {
	results := []DiffResult{
		{RelativePath: "a.md", Decision: DecisionNone},
		{RelativePath: "b.md", Decision: DecisionPush},
		{RelativePath: "c.md", Decision: DecisionNone},
	}

	ops := Plan(results)
	if len(ops) != 1 {
		t.Fatalf("got %d ops, want 1", len(ops))
	}
	if ops[0].RelativePath != "b.md" {
		t.Errorf("RelativePath = %q, want %q", ops[0].RelativePath, "b.md")
	}
}

func TestPlan_OrdersShallowerPathsFirst(t *testing.T) {
	results := []DiffResult{
		{RelativePath: "deep/nested/file.md", Decision: DecisionCreateRemote},
		{RelativePath: "root.md", Decision: DecisionCreateRemote},
		{RelativePath: "sub/file.md", Decision: DecisionCreateRemote},
	}

	ops := Plan(results)
	want := []string{"root.md", "sub/file.md", "deep/nested/file.md"}
	if len(ops) != len(want) {
		t.Fatalf("got %d ops, want %d", len(ops), len(want))
	}
	for i, w := range want {
		if ops[i].RelativePath != w {
			t.Errorf("ops[%d].RelativePath = %q, want %q", i, ops[i].RelativePath, w)
		}
	}
}

func TestPlan_SameDepthOrderedAlphabetically(t *testing.T) {
	results := []DiffResult{
		{RelativePath: "b.md", Decision: DecisionCreateRemote},
		{RelativePath: "a.md", Decision: DecisionCreateRemote},
	}
	ops := Plan(results)
	if ops[0].RelativePath != "a.md" || ops[1].RelativePath != "b.md" {
		t.Errorf("got order %q, %q; want a.md, b.md", ops[0].RelativePath, ops[1].RelativePath)
	}
}

func TestPlan_EmptyInputProducesEmptyOutput(t *testing.T) {
	ops := Plan(nil)
	if len(ops) != 0 {
		t.Errorf("got %d ops, want 0", len(ops))
	}
}
