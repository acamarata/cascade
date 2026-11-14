package hj

import (
	"testing"
)

func TestMigrateQueuedToOpenNode(t *testing.T) {
	nodes := []Node{
		{Name: "A", FreeSlots: 0},
		{Name: "B", FreeSlots: 2},
	}
	queue := []Task{
		{ID: "t1", Node: "A", Running: false, Age: 10},
		{ID: "t2", Node: "A", Running: false, Age: 5},
		{ID: "t3", Node: "A", Running: false, Age: 20},
	}
	moves := Rebalance(nodes, queue)
	if len(moves) != 2 {
		t.Fatalf("expected 2 moves, got %d", len(moves))
	}
	if moves[0].TaskID != "t3" || moves[0].From != "A" || moves[0].To != "B" {
		t.Errorf("unexpected move 0: %v", moves[0])
	}
	if moves[1].TaskID != "t1" || moves[1].From != "A" || moves[1].To != "B" {
		t.Errorf("unexpected move 1: %v", moves[1])
	}
}

func TestNoMigrationWithoutCapacity(t *testing.T) {
	nodes := []Node{
		{Name: "A", FreeSlots: 0},
		{Name: "B", FreeSlots: 0},
	}
	queue := []Task{
		{ID: "t1", Node: "A", Running: false, Age: 10},
	}
	moves := Rebalance(nodes, queue)
	if len(moves) != 0 {
		t.Fatalf("expected 0 moves, got %d", len(moves))
	}
}

func TestMigrationCappedByFreeSlots(t *testing.T) {
	nodes := []Node{
		{Name: "A", FreeSlots: 0},
		{Name: "B", FreeSlots: 1},
	}
	queue := []Task{
		{ID: "t1", Node: "A", Running: false, Age: 10},
		{ID: "t2", Node: "A", Running: false, Age: 5},
		{ID: "t3", Node: "A", Running: false, Age: 20},
		{ID: "t4", Node: "A", Running: false, Age: 15},
		{ID: "t5", Node: "A", Running: false, Age: 25},
	}
	moves := Rebalance(nodes, queue)
	if len(moves) != 1 {
		t.Fatalf("expected 1 move, got %d", len(moves))
	}
	if moves[0].TaskID != "t5" || moves[0].From != "A" || moves[0].To != "B" {
		t.Errorf("unexpected move 0: %v", moves[0])
	}
}

func TestRunningTaskNeverMoves(t *testing.T) {
	nodes := []Node{
		{Name: "A", FreeSlots: 0},
		{Name: "B", FreeSlots: 2},
	}
	queue := []Task{
		{ID: "t1", Node: "A", Running: true, Age: 10},
	}
	moves := Rebalance(nodes, queue)
	if len(moves) != 0 {
		t.Fatalf("expected 0 moves, got %d", len(moves))
	}
}
