package client

import (
	"errors"
	"sort"
	"testing"

	v1 "github.com/pm0/pm0/api/v1"
)

var (
	errTest  = errors.New("first")
	errTest2 = errors.New("second")
)

func TestSplitSelectorSequential(t *testing.T) {
	sel := &v1.Selector{PmIds: []int32{1, 2, 3}}
	if got := splitSelector(sel, 1); len(got) != 1 || got[0] != sel {
		t.Fatalf("n=1 must return the selector unchanged, got %v", got)
	}
	single := &v1.Selector{PmIds: []int32{7}}
	if got := splitSelector(single, 8); len(got) != 1 {
		t.Fatalf("single-app selector must not fan out, got %v", got)
	}
	all := &v1.Selector{All: true}
	if got := splitSelector(all, 8); len(got) != 1 {
		t.Fatalf("all must not fan out in splitSelector (resolved via List), got %v", got)
	}
}

func TestSplitSelectorBounded(t *testing.T) {
	sel := &v1.Selector{PmIds: []int32{1, 2, 3, 4, 5}, Names: []string{"a", "b"}}
	got := splitSelector(sel, 4)
	if len(got) != 4 {
		t.Fatalf("want 4 RPC selectors, got %d", len(got))
	}
	var ids []int32
	var names []string
	for _, s := range got {
		ids = append(ids, s.GetPmIds()...)
		names = append(names, s.GetNames()...)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	sort.Strings(names)
	if len(ids) != 5 || len(names) != 2 {
		t.Fatalf("fan-out lost targets: ids=%v names=%v", ids, names)
	}
}

func TestMergeRestartSortedDedup(t *testing.T) {
	merged, err := mergeRestart([]restartRes{
		{resp: &v1.RestartProcessResponse{AffectedPmIds: []int32{3, 1}}},
		{resp: &v1.RestartProcessResponse{AffectedPmIds: []int32{1, 2}}},
	})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	want := []int32{1, 2, 3}
	got := merged.GetAffectedPmIds()
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestMergeStopFirstError(t *testing.T) {
	if _, err := mergeStop([]stopRes{{err: errTest}, {err: errTest2}}); err != errTest {
		t.Fatalf("want first error, got %v", err)
	}
	merged, err := mergeStop([]stopRes{
		{resp: &v1.StopProcessResponse{AffectedPmIds: []int32{5}}},
		{err: errTest},
	})
	if err != errTest {
		t.Fatalf("partial success must still surface the error, got %v", err)
	}
	if len(merged.GetAffectedPmIds()) != 1 {
		t.Fatalf("partial ids lost: %v", merged.GetAffectedPmIds())
	}
}
