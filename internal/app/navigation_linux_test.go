//go:build linux

package app

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/nullco/lazyrun/internal/model"
	"github.com/nullco/lazyrun/internal/testutil"
	"github.com/nullco/lazyrun/internal/transport"
	"golang.org/x/sys/unix"
)

func navigationProject(t *testing.T) (*integration, model.Run) {
	t.Helper()
	command, err := testutil.FixtureCommand("paged")
	if err != nil {
		t.Fatal(err)
	}
	f := newIntegration(t, fmt.Sprintf("version: 1\nlogs: {maxBytes: 4194304, tail: 20, timestamps: false}\ntasks:\n  archive:\n    command: %q\n", command))
	run, err := f.connection.Client.Start(context.Background(), "archive", nil)
	if err != nil {
		t.Fatal(err)
	}
	f.remember(run)
	run = f.finished("archive", run)
	if run.LogError != "" || run.LogEnd < 1000000 {
		t.Fatal("fixture lost output", run)
	}
	return f, run
}
func TestLogNavigationWindowAndSearchThroughIPC(t *testing.T) {
	f, run := navigationProject(t)
	c := f.connection.Client
	ctx := context.Background()
	tail, err := c.TailLogs(ctx, "archive", run.ID, 20, 64*1024)
	if err != nil || bytes.Contains(tail.Data, []byte("EARLY")) || !bytes.Contains(tail.Data, []byte("LATE")) {
		t.Fatal(tail, err)
	}
	first, err := c.WindowLogs(ctx, "archive", run.ID, 0, 0, 64*1024)
	if err != nil || !bytes.HasPrefix(first.Data, []byte("EARLY needle\n")) || first.First != 0 || first.End != run.LogEnd {
		t.Fatal(first, err)
	}
	request := model.LogSearchRequest{Query: "needle"}
	var matches []model.LogMatch
	for range 100 {
		r, err := c.SearchLogs(ctx, "archive", run.ID, request)
		if err != nil {
			t.Fatal(err)
		}
		if r.RunID != run.ID || r.Scanned > 256*1024 || len(r.Matches) > 64 {
			t.Fatal("unbounded/incorrect search reply", r)
		}
		matches = append(matches, r.Matches...)
		if r.Done {
			break
		}
		request.State = r.State
	}
	if len(matches) != 3 {
		t.Fatal(matches)
	}
	for i, word := range []string{"EARLY", "MIDDLE", "LATE"} {
		page, err := c.WindowLogs(ctx, "archive", run.ID, matches[i].Cursor, 1024, 64*1024)
		if err != nil || !bytes.Contains(page.Data, []byte(word+" needle")) {
			t.Fatal(word, page, err)
		}
	}
	before, cutoff := matches[2].End, matches[2].Cursor
	request = model.LogSearchRequest{Query: "needle", Before: &before, MatchBefore: &cutoff, Last: true}
	for range 100 {
		r, err := c.SearchLogs(ctx, "archive", run.ID, request)
		if err != nil {
			t.Fatal(err)
		}
		if r.Done {
			if len(r.Matches) != 1 || r.Matches[0] != matches[1] {
				t.Fatal(r)
			}
			break
		}
		request.State = r.State
	}
	for _, query := range []string{"", "bad\n"} {
		_, err := c.SearchLogs(ctx, "archive", run.ID, model.LogSearchRequest{Query: query})
		if e, ok := err.(*transport.Error); !ok || e.Code != transport.CodeInvalid {
			t.Fatal(query, err)
		}
	}
	if _, err := c.WindowLogs(ctx, "archive", run.ID, run.LogEnd+1, 0, 10); err == nil {
		t.Fatal("future window cursor accepted")
	}
	if _, err := c.SearchLogs(ctx, "archive", "another-run", model.LogSearchRequest{Query: "needle"}); err == nil {
		t.Fatal("search accepted a different run")
	}
}

func TestDashboardWrapsLongLinesScrollsAndReflowsAfterResize(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	text := "WRAP-BEGIN " + strings.Repeat("x", 800) + " WRAP-MIDDLE " + strings.Repeat("y", 800) + " WRAP-END\n"
	command := "printf '%s' '" + text + "'"
	f := newIntegration(t, fmt.Sprintf("version: 1\nlogs: {timestamps: false}\ntasks:\n  wrapped:\n    command: %q\n", command))
	run, err := f.connection.Client.Start(context.Background(), "wrapped", nil)
	if err != nil {
		t.Fatal(err)
	}
	f.remember(run)
	run = f.finished("wrapped", run)
	cmd, master := terminalClient(t, f.root)
	out := watchTerminal(t, master)
	eventuallyIntegration(t, func() bool { return out.contains("Tasks") })
	keys(t, master, "3\r")
	// The suffix is far outside the first pane-width of this single line.
	// No horizontal scrolling or search is needed to see it in follow mode.
	eventuallyIntegration(t, func() bool { return out.contains("WRAP-END") })
	out.clear()
	keys(t, master, "\x1b[H")
	eventuallyIntegration(t, func() bool { return out.contains("WRAP-BEGIN") })
	out.clear()
	keys(t, master, "\x1b[6~")
	eventuallyIntegration(t, func() bool { return out.contains("WRAP-END") })
	out.clear()
	keys(t, master, "G")
	eventuallyIntegration(t, func() bool { return out.contains("following") })
	out.clear()
	if err := unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 18, Col: 70}); err != nil {
		t.Fatal(err)
	}
	eventuallyIntegration(t, func() bool { return out.contains("WRAP-END") })
	keys(t, master, "q")
	if err := waitClient(t, cmd); err != nil {
		t.Fatal(err)
	}
	if findRun(f.state(), "wrapped").ID != run.ID || f.logs("wrapped", run) != text {
		t.Fatal("wrapping changed the run or its stored output")
	}
}

func TestDashboardPagesToBeginningAndSearchesAllRetainedLogs(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	f, run := navigationProject(t)
	cmd, master := terminalClient(t, f.root)
	out := watchTerminal(t, master)
	t.Cleanup(func() {
		if t.Failed() {
			out.mu.Lock()
			defer out.mu.Unlock()
			t.Logf("terminal output prefix: %q", out.data[:min(10000, len(out.data))])
			t.Logf("terminal output suffix: %q", out.data[max(0, len(out.data)-2000):])
		}
	})
	eventuallyIntegration(t, func() bool { return out.contains("Tasks") })
	keys(t, master, "3\r")
	eventuallyIntegration(t, func() bool { return out.contains("LATE needle") })
	out.clear()
	keys(t, master, "\x1b[5~\x1b[5~\x1b[5~") // PgUp beyond the now-wrapped 20-line tail
	eventuallyIntegration(t, func() bool { return out.contains("history") })
	out.clear()
	keys(t, master, "\x1b[H") // Home: first retained byte, not just buffer top
	eventuallyIntegration(t, func() bool { return out.contains("EARLY needle") })
	out.clear()
	keys(t, master, "/MATCHTARGET\r")
	eventuallyIntegration(t, func() bool { return out.contains("long-match-suffix") })
	out.clear()
	keys(t, master, "/needle\r")
	eventuallyIntegration(t, func() bool { return out.contains("search \"needle\"") && out.contains("EARLY") })
	out.clear()
	// Log search/navigation must not capture list-pane keys. If / opened a
	// global prompt, ? would become search text instead of opening Help; if G
	// cleared the log search here, returning to Logs would lose this match.
	keys(t, master, "3/nNG?")
	eventuallyIntegration(t, func() bool { return out.contains("Navigation") })
	out.clear()
	keys(t, master, "?\r")
	eventuallyIntegration(t, func() bool { return out.contains("search \"needle\"") && out.contains("EARLY") })
	out.clear()
	keys(t, master, "n")
	eventuallyIntegration(t, func() bool { return out.contains("MIDDLE") })
	out.clear()
	keys(t, master, "n")
	eventuallyIntegration(t, func() bool { return out.contains("LATE") })
	out.clear()
	keys(t, master, "N")
	eventuallyIntegration(t, func() bool { return out.contains("MIDDLE") })
	out.clear()
	keys(t, master, "G") // G clears search and returns to a fresh live tail
	eventuallyIntegration(t, func() bool { return out.contains("LATE needle") })
	keys(t, master, "/qSsrneedle") // editing must not quit or execute lifecycle hotkeys
	eventuallyIntegration(t, func() bool { return out.contains("Filter:") })
	if findRun(f.state(), "archive").ID != run.ID {
		t.Fatal("search editing reran the command")
	}
	out.clear()
	keys(t, master, "\x03") // Ctrl-C closes the prompt, not the client
	eventuallyIntegration(t, func() bool { return out.contains("\x1b[?25l") })
	keys(t, master, "q")
	if err := waitClient(t, cmd); err != nil {
		t.Fatal(err)
	}
	if findRun(f.state(), "archive").ID != run.ID {
		t.Fatal("navigation changed the run")
	}
}
