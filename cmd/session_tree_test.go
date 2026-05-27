package cmd

import (
	"reflect"
	"testing"
)

func TestBuildSessionTreeRowsNestedSessions(t *testing.T) {
	rows := buildSessionTreeRows([]string{
		"g/patches/feat/mar24-new-dev-cli",
		"g/position-exercise",
		"g/SHIP",
		"g/CLIs",
	})

	got := make([]string, 0, len(rows))
	for _, row := range rows {
		got = append(got, row.label())
	}

	want := []string{
		"├── patches",
		"│   └── feat",
		"│       └── mar24-new-dev-cli",
		"├── position-exercise",
		"├── SHIP",
		"└── CLIs",
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected tree labels:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestBuildSessionTreeRowsSplitsBranchAndSegment(t *testing.T) {
	rows := buildSessionTreeRows([]string{"g/foo/bar"})
	// Leaf row is the second row (foo, then bar).
	leaf := rows[1]
	if leaf.segment != "bar" {
		t.Fatalf("segment = %q, want bar", leaf.segment)
	}
	if leaf.branch+leaf.segment != leaf.label() {
		t.Fatalf("branch+segment %q != label %q", leaf.branch+leaf.segment, leaf.label())
	}
}

func TestBuildSessionTreeRowsKeepsSessionOnBranchNode(t *testing.T) {
	rows := buildSessionTreeRows([]string{"g/foo", "g/foo/bar"})
	if len(rows) != 2 {
		t.Fatalf("row count = %d, want 2", len(rows))
	}
	if rows[0].sessionName != "g/foo" {
		t.Fatalf("branch node session = %q, want g/foo", rows[0].sessionName)
	}
	if rows[1].sessionName != "g/foo/bar" {
		t.Fatalf("leaf node session = %q, want g/foo/bar", rows[1].sessionName)
	}
}

func TestBuildSessionTreeRowsBranchTargetsFirstDescendant(t *testing.T) {
	rows := buildSessionTreeRows([]string{"g/foo/bar", "g/foo/baz", "g/qux"})
	if rows[0].defaultTarget != "g/foo/bar" {
		t.Fatalf("branch target = %q, want g/foo/bar", rows[0].defaultTarget)
	}
	if rows[0].leafCount != 2 {
		t.Fatalf("branch leaf count = %d, want 2", rows[0].leafCount)
	}
}
