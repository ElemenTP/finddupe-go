package pipeline

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"finddupe/internal/compress"
	"finddupe/internal/config"
	"finddupe/internal/dupe"
)

// maxPromptAttempts bounds how often an unusable answer is re-asked before the
// group is left alone; a terminal that sends control characters must not be able
// to spin the coordinator.
const maxPromptAttempts = 3

// interactiveChooser asks the user which file of each content group to keep.
//
// It runs in the coordinator goroutine, at the moment the detector decides a
// group: every member is known, nothing is in flight, and the answer applies to
// the whole group. That is also why the prompt can be a plain read from stdin —
// no other stage is waiting for this goroutine while the user thinks.
type interactiveChooser struct {
	action config.Action
	report *reportWriter
	input  *bufio.Reader

	// autoDefault, once set, keeps the default (policy) choice for every later
	// group without asking — the "a" answer. It is a rule, not a path: the same
	// path cannot be a member of a different content group.
	autoDefault bool

	// stopped, once set, leaves every later group untouched (the "q" answer).
	stopped bool
}

// newInteractiveChooser creates a chooser reading answers from in. The caller is
// responsible for checking that in is a terminal.
func newInteractiveChooser(action config.Action, report *reportWriter, in io.Reader) *interactiveChooser {
	return &interactiveChooser{
		action: action,
		report: report,
		input:  bufio.NewReader(in),
	}
}

// Choose implements [dupe.KeeperChooser].
func (c *interactiveChooser) Choose(members []dupe.FileInfo) (dupe.FileInfo, bool) {
	if c.stopped {
		return dupe.FileInfo{}, false
	}
	if c.autoDefault {
		// members[0] is the policy's choice for this group.
		return members[0], true
	}

	c.printGroup(members)
	for range maxPromptAttempts {
		c.report.flush()
		c.printPrompt(len(members))

		line, err := c.input.ReadString('\n')
		if err != nil && line == "" {
			// stdin ended (or failed): stop asking and leave the rest alone.
			c.stopped = true
			return dupe.FileInfo{}, false
		}

		answer := strings.TrimSpace(line)
		switch answer {
		case "a":
			c.autoDefault = true
			return members[0], true
		case "s":
			return dupe.FileInfo{}, false
		case "q":
			c.stopped = true
			return dupe.FileInfo{}, false
		default:
			if index, convErr := strconv.Atoi(answer); convErr == nil && index >= 1 && index <= len(members) {
				return members[index-1], true
			}
			fmt.Fprintf(c.report.w, "  please answer 1-%d, a, s or q\n", len(members))
		}
	}

	// Too many unusable answers: do not touch the group.
	return dupe.FileInfo{}, false
}

// printGroup lists the members the user has to choose between.
func (c *interactiveChooser) printGroup(members []dupe.FileInfo) {
	c.report.printf("Identical content, %d files (%s):\n", len(members), c.actionLabel())
	for i, member := range members {
		compressed := ""
		if compress.IsCompressed(member) {
			compressed = ", compressed"
		}
		reference := ""
		if member.IsRef {
			reference = ", reference"
		}
		c.report.printf("  %d) %s\n     %s, %s, %d hardlink(s)%s%s\n",
			i+1, resultPath(member.Path), formatSize(member.Size),
			member.ModTime.Format("2006-01-02 15:04:05"), member.NumLinks, compressed, reference)
	}
}

// printPrompt explains the answers. The prompt itself goes to stderr so the
// report on stdout stays a machine-readable stream.
func (c *interactiveChooser) printPrompt(count int) {
	fmt.Fprintf(os.Stderr,
		"keep which file? 1-%d to keep it and eliminate the others, "+
			"a = keep the default choice for this and every later group, "+
			"s = skip this group, q = stop deciding (leave the rest untouched): ", count)
}

// actionLabel describes what happens to the files that are not kept.
func (c *interactiveChooser) actionLabel() string {
	switch c.action {
	case config.ActionDelete:
		return "the others will be deleted"
	case config.ActionHardlink:
		return "the others will be replaced with hardlinks to the kept file"
	case config.ActionCoWClone:
		return "the others will be replaced with CoW clones of the kept file"
	case config.ActionReport:
		return "report only"
	}
	return "report only"
}

// errInteractiveNeedsTerminal is returned when --interactive is used without a
// terminal to answer on.
var errInteractiveNeedsTerminal = errors.New("--interactive needs a terminal on stdin to ask about each group")
