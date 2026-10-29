package subs

import "testing"

func TestFromNodesServesOnlyItsNodes(t *testing.T) {
	off := FromNodes{Nodes: []int64{1, 2}}
	if off.Serves(1) {
		t.Fatal("a disabled setting serves")
	}
	on := FromNodes{Enabled: true, Nodes: []int64{2, 5}}
	if !on.Serves(5) || !on.Serves(2) || on.Serves(1) || on.Serves(0) {
		t.Fatalf("serves: %+v", on)
	}
	none := FromNodes{Enabled: true}
	if none.Serves(1) {
		t.Fatal("no chosen node serves")
	}
}

func TestFromNodesCheck(t *testing.T) {
	exists := func(id int64) bool { return id == 1 || id == 2 }
	if err := (FromNodes{Nodes: []int64{1, 2}}).Check(exists); err != nil {
		t.Fatalf("a good setting: %v", err)
	}
	if err := (FromNodes{Nodes: []int64{1, 1}}).Check(exists); err == nil || err.Error() != "subs_node_unknown" {
		t.Fatalf("a repeated node: %v", err)
	}
	if err := (FromNodes{Nodes: []int64{3}}).Check(exists); err == nil || err.Error() != "subs_node_unknown" {
		t.Fatalf("a node that is not there: %v", err)
	}
	if err := (FromNodes{Nodes: []int64{0}}).Check(exists); err == nil {
		t.Fatal("node 0 passes")
	}
	many := FromNodes{}
	for i := int64(1); i <= MaxSubNodes+1; i++ {
		many.Nodes = append(many.Nodes, i)
	}
	if err := many.Check(func(int64) bool { return true }); err == nil || err.Error() != "subs_nodes_many" {
		t.Fatalf("too many nodes: %v", err)
	}
}
