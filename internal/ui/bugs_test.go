package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"orkestra/internal/worktree"
)

func TestCowStaysStableOnRowRefresh(t *testing.T) {
	m := testModel()
	m.cow = []string{"original fortune"}
	m.cursor = 1
	rows := append([]worktree.Row{m.rows[1]}, m.rows[0])
	m.Update(rowsMsg(rows))
	if strings.Join(m.cow, "") != "original fortune" {
		t.Fatal("refresh changed fortune")
	}
	if selected, _ := m.selected(); selected.Task != "other" {
		t.Fatalf("refresh moved selection: %+v", selected)
	}
}

func TestRefreshCannotRetargetDeletionConfirmation(t *testing.T) {
	m := testModel()
	m.mode = modeConfirmEnd
	m.confirmYes = true
	m.Update(rowsMsg([]worktree.Row{m.rows[1]}))
	if m.mode != modeList || m.confirmYes {
		t.Fatal("confirmation survived disappearance of target")
	}
}

func TestFrameFitsEveryMode(t *testing.T) {
	for _, mode := range []mode{modeList, modeConfirmEnd, modePickRepo, modeTaskName} {
		for _, width := range []int{1, 40, 120, 240} {
			m := testModel()
			m.mode = mode
			m.width = width
			m.height = 12
			m.cow = []string{strings.Repeat("界", 40), "orc"}
			m.repos = []string{strings.Repeat("界", 80)}
			m.pickedRepo = strings.Repeat("界", 80)
			m.err = strings.Repeat("problem", 80)
			out := m.View()
			if !utf8.ValidString(out) {
				t.Fatal("invalid UTF-8 frame")
			}
			lines := strings.Split(out, "\n")
			if len(lines) > m.height {
				t.Fatalf("frame has %d lines for height %d", len(lines), m.height)
			}
			for _, line := range lines {
				if ansi.StringWidth(line) > width {
					t.Fatalf("mode %d: line exceeds %d cells", mode, width)
				}
			}
		}
	}
}

func TestCowIsCompleteOrHidden(t *testing.T) {
	m := testModel()
	m.width = 240
	m.height = 40
	for i := 0; i < 16; i++ {
		m.cow = append(m.cow, "cow")
	}
	m.cow[15] = "orc feet"
	if !strings.Contains(m.View(), "orc feet") {
		t.Fatal("cow cropped despite available space")
	}
	m.height = 18
	if strings.Contains(m.View(), "cow") {
		t.Fatal("partially visible cow in short terminal")
	}
}

func TestUnicodeEditingAndWrapping(t *testing.T) {
	m := testModel()
	m.filter = "café界"
	m.handleKey(tea.KeyMsg{Type: tea.KeyBackspace})
	if m.filter != "café" {
		t.Fatalf("filter=%q", m.filter)
	}
	wrapped := foldText(strings.Repeat("界é", 20), 8)
	if !utf8.ValidString(wrapped) {
		t.Fatal("wrap split rune")
	}
	for _, line := range strings.Split(wrapped, "\n") {
		if ansi.StringWidth(line) > 8 {
			t.Fatalf("wrap too wide: %q", line)
		}
	}
	if got := pad(trunc("界界界", 4), 4); ansi.StringWidth(got) != 4 || !utf8.ValidString(got) {
		t.Fatalf("invalid padded text %q", got)
	}
}

func TestShellCleanupArgumentsStayLiteral(t *testing.T) {
	input := "task'$(printf injected)`printf injected`"
	out, err := exec.Command("bash", "-c", "printf '%s' "+quoteShell(input)).Output()
	if err != nil || string(out) != input {
		t.Fatalf("shell quoting: %v %q", err, out)
	}
}

func TestRealCowSidebar(t *testing.T) {
	for _, binary := range []string{"fortune", "cowsay"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skip(binary + " unavailable")
		}
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ORK_ROOT", root)
	if _, err := os.Stat(filepath.Join(root, "orc.cow")); err != nil {
		t.Fatal(err)
	}
	cow := cowSidebar()
	if len(cow) == 0 || !strings.Contains(strings.Join(cow, "\n"), "_______________") {
		t.Fatalf("no orc: %v", cow)
	}
}
