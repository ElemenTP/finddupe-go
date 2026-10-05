package pipeline //nolint:testpackage // exercises the unexported chooser

import (
	"bytes"
	"strings"
	"testing"

	"finddupe/internal/config"
	"finddupe/internal/dupe"
)

// members builds a group of three equally sized files.
func members() []dupe.FileInfo {
	return []dupe.FileInfo{
		{Path: "/a", Size: 10, NumLinks: 1},
		{Path: "/b", Size: 10, NumLinks: 1},
		{Path: "/c", Size: 10, NumLinks: 1},
	}
}

// newTestChooser returns a chooser reading the given answers.
func newTestChooser(t *testing.T, action config.Action, answers string) (*interactiveChooser, *bytes.Buffer) {
	t.Helper()
	var seen bytes.Buffer
	return newInteractiveChooser(action, newReportWriter(&seen), strings.NewReader(answers)), &seen
}

func TestInteractiveChooser_KeepsByNumber(t *testing.T) {
	t.Parallel()

	chooser, seen := newTestChooser(t, config.ActionDelete, "2\n")
	keeper, ok := chooser.Choose(members())

	if !ok || keeper.Path != "/b" {
		t.Fatalf("Choose = (%q, %v), want /b", keeper.Path, ok)
	}
	// The group listing must name every member and the action's consequence.
	rendered := seen.String()
	for _, want := range []string{"1) /a", "2) /b", "3) /c", "will be deleted"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("group listing is missing %q:\n%s", want, rendered)
		}
	}
}

func TestInteractiveChooser_ApplyToEveryLaterGroup(t *testing.T) {
	t.Parallel()

	chooser, _ := newTestChooser(t, config.ActionHardlink, "a\n")
	first, ok := chooser.Choose(members())
	if !ok || first.Path != "/a" {
		t.Fatalf("Choose = (%q, %v), want the default /a", first.Path, ok)
	}

	// No further input is read: every later group keeps its own default member,
	// because the same path cannot belong to a different content group.
	second, ok := chooser.Choose([]dupe.FileInfo{{Path: "/x", Size: 10}, {Path: "/y", Size: 10}})
	if !ok || second.Path != "/x" {
		t.Fatalf("second Choose = (%q, %v), want /x (its own default)", second.Path, ok)
	}
	third, ok := chooser.Choose([]dupe.FileInfo{{Path: "/z", Size: 10}, {Path: "/w", Size: 10}})
	if !ok || third.Path != "/z" {
		t.Fatalf("third Choose = (%q, %v), want /z (its own default)", third.Path, ok)
	}
}

func TestInteractiveChooser_SkipAndKeep(t *testing.T) {
	t.Parallel()

	chooser, _ := newTestChooser(t, config.ActionCoWClone, "s\n2\n")
	if _, ok := chooser.Choose(members()); ok {
		t.Fatal("the skipped group must be left alone")
	}
	keeper, ok := chooser.Choose(members())
	if !ok || keeper.Path != "/b" {
		t.Fatalf("Choose = (%q, %v), want /b after the skip", keeper.Path, ok)
	}
}

func TestInteractiveChooser_Stop(t *testing.T) {
	t.Parallel()

	chooser, _ := newTestChooser(t, config.ActionDelete, "q\n")
	if _, ok := chooser.Choose(members()); ok {
		t.Fatal("q must leave the group alone")
	}
	if _, ok := chooser.Choose(members()); ok {
		t.Fatal("q must leave every later group alone without asking")
	}
}

func TestInteractiveChooser_RetriesUnusableAnswers(t *testing.T) {
	t.Parallel()

	chooser, seen := newTestChooser(t, config.ActionDelete, "x\n0\n99\n")
	if _, ok := chooser.Choose(members()); ok {
		t.Fatal("unusable answers must leave the group alone")
	}
	if !strings.Contains(seen.String(), "please answer 1-3") {
		t.Errorf("the re-ask hint is missing:\n%s", seen.String())
	}
}

func TestInteractiveChooser_EndOfInputStopsAsking(t *testing.T) {
	t.Parallel()

	chooser, _ := newTestChooser(t, config.ActionDelete, "")
	if _, ok := chooser.Choose(members()); ok {
		t.Fatal("end of input must leave the group alone")
	}
	if !chooser.stopped {
		t.Fatal("end of input must stop further questions")
	}
}
