package store

import (
	"context"
	"testing"
)

func TestBoardSince(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	if err := s.SetBoardSince(ctx, "p:w", BoardState{State: "stalled|Stalled", Since: 100}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetBoardSince(ctx, "p:w", BoardState{State: "working|Readying for merge", Since: 200}); err != nil {
		t.Fatal(err)
	}
	got, err := s.BoardSince(ctx)
	if err != nil || got["p:w"] != (BoardState{State: "working|Readying for merge", Since: 200}) {
		t.Fatalf("got %v %v", got, err)
	}
	if err := s.SetBoardSince(ctx, "p:w", BoardState{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.BoardSince(ctx); len(got) != 0 {
		t.Fatalf("not dropped: %v", got)
	}
}
