package tmux

import "testing"

func TestParsePanes(t *testing.T) {
	out := "mytask:0.0\t/home/u/worktrees/repo/mytask\tnode\t123\n" +
		"other:1.2\t/tmp\tzsh\t456\n" +
		"\n" +
		"badline\n"
	panes := ParsePanes(out)
	if len(panes) != 2 {
		t.Fatalf("got %d panes", len(panes))
	}
	p := panes[0]
	if p.Session != "mytask" || p.Target != "mytask:0.0" || p.CWD != "/home/u/worktrees/repo/mytask" || p.Cmd != "node" || p.PID != 123 {
		t.Errorf("pane[0] = %+v", p)
	}
}
