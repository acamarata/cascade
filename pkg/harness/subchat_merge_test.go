package harness

import "testing"

func TestMergeWorkingSubChatReturnsD6086(t *testing.T) {
	src := &SubChat{
		ID:       "sc-1",
		State:    SubChatWorking,
		Messages: []string{"msg1"},
	}
	dst := &SubChat{
		ID:       "sc-2",
		State:    SubChatIdle,
		Messages: []string{"msg2"},
	}

	err := MergeSubChat(src, dst)
	if err == nil {
		t.Fatal("expected error when merging working sub-chat, got nil")
	}

	me, ok := err.(*MergeError)
	if !ok {
		t.Fatalf("expected *MergeError, got %T", err)
	}
	if me.Code != "D6086" {
		t.Errorf("expected error code D6086, got %s", me.Code)
	}
}

func TestMergeWorkingSubChatLeavesStateUnchanged(t *testing.T) {
	src := &SubChat{
		ID:       "sc-1",
		State:    SubChatWorking,
		Messages: []string{"msg1"},
	}
	dst := &SubChat{
		ID:       "sc-2",
		State:    SubChatIdle,
		Messages: []string{"msg2"},
	}

	_ = MergeSubChat(src, dst)

	if src.State != SubChatWorking {
		t.Errorf("expected src state to remain working, got %s", src.State)
	}
	if dst.State != SubChatIdle {
		t.Errorf("expected dst state to remain idle, got %s", dst.State)
	}
	if src.MessageCount() != 1 {
		t.Errorf("expected src message count to remain 1, got %d", src.MessageCount())
	}
	if dst.MessageCount() != 1 {
		t.Errorf("expected dst message count to remain 1, got %d", dst.MessageCount())
	}
}
