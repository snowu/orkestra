package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"orkestra/internal/config"
	"orkestra/internal/worktree"
)

func testModel() *Model {
	m := New(config.Config{WorktreeRoots: []string{"/nowhere"}})
	m.width, m.height = 120, 40
	m.rows = []worktree.Row{
		{Repo: "repoA", Task: "task-one", Branch: "task-one", Path: "/w/repoA/task-one", Live: true, Session: "task-one", Agent: "running", Cmd: "node"},
		{Repo: "repoB", Task: "other", Branch: "fix", Path: "/w/repoB/other"},
	}
	m.applyFilter()
	return m
}

func TestViewRendersRows(t *testing.T) {
	v := testModel().View()
	for _, want := range []string{"REPO", "repoA", "task-one", "live", "running", "repoB", "idle"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q", want)
		}
	}
	// branch == task renders as "="
	if !strings.Contains(v, "=") {
		t.Error("branch shorthand '=' missing")
	}
}

func TestFilterNarrows(t *testing.T) {
	m := testModel()
	m.filter = "other"
	m.applyFilter()
	if len(m.visible) != 1 || m.rows[m.visible[0]].Task != "other" {
		t.Fatalf("filter failed: %v", m.visible)
	}
}

func TestEnterReturnsAttach(t *testing.T) {
	m := testModel()
	_, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter should quit")
	}
	if m.result.Action != ActionAttach || m.result.Task != "task-one" {
		t.Errorf("result = %+v", m.result)
	}
}

func TestConfirmDefaultsToNo(t *testing.T) {
	m := testModel()
	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlX})
	if m.mode != modeConfirmEnd || m.confirmYes {
		t.Fatalf("mode=%v yes=%v", m.mode, m.confirmYes)
	}
	// reflexive enter = safe "no", back to list, nothing destroyed
	m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != modeList {
		t.Error("enter on 'no' should return to list")
	}
}
