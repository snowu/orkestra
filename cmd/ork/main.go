// ork: control/jump between worktree agents running in tmux.
//
// cd contract (shared with the ork.sh shell wrapper): the ONLY thing this
// program ever writes to stdout is the directory to cd into, as the last
// line, when there is one. Everything else — the TUI, errors, progress —
// goes to stderr/tty so the wrapper's $(...) capture never swallows it.
package main

import (
	"fmt"
	"os"
)

var version = "dev"

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		runTUI()
		return
	}
	switch args[0] {
	case "--version":
		fmt.Fprintln(os.Stderr, "ork "+version)
	case "new-task":
		if len(args) < 2 {
			fatal("usage: ork new-task <task-name>")
		}
		runNewTask(args[1])
	case "end-task":
		task := ""
		if len(args) > 1 {
			task = args[1]
		}
		runEndTask(task)
	case "_end-task":
		// Internal: cleanup with explicit repo/task, run inside the temp
		// "ork-end-*" tmux session the TUI spawns so its output can be
		// tailed in the live pane.
		if len(args) < 3 {
			fatal("usage: ork _end-task <repo> <task>")
		}
		runEndTaskDirect(args[1], args[2])
	default:
		fatal("usage: ork [new-task <name> | end-task [name] | --version]")
	}
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "ork: "+msg)
	os.Exit(1)
}
