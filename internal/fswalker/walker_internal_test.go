package fswalker

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// TestSendResult covers the rule that keeps a walker goroutine from parking on a
// send after its consumer has gone away.
func TestSendResult(t *testing.T) {
	t.Parallel()

	t.Run("delivers when the consumer reads", func(t *testing.T) {
		t.Parallel()

		ch := make(chan Result, 1)
		if !sendResult(context.Background(), ch, Result{}) {
			t.Fatal("sendResult reported failure for a readable channel")
		}
	})

	t.Run("abandons the send when the walk is cancelled", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		ch := make(chan Result) // nobody will ever read
		done := make(chan bool, 1)

		go func() {
			done <- sendResult(ctx, ch, Result{Err: errors.New("walk error")})
		}()
		cancel()

		select {
		case delivered := <-done:
			if delivered {
				t.Error("sendResult delivered a result nobody read")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("sendResult blocked after cancellation")
		}
	})
}

// TestSplitPattern_RootWildcard covers a glob whose only prefix is the volume
// root. The separator before the wildcard used to leave an empty base directory,
// and [os.Stat] of the empty string answers ENOENT, so "/*" and "/*.conf"
// reported that nothing matched no matter what the root contained.
func TestSplitPattern_RootWildcard(t *testing.T) {
	t.Parallel()

	// The bare drive letter is a Windows form: "C:" alone names that drive's
	// current directory, so the separator has to stay part of the base.
	driveRoot := "C:" + string(filepath.Separator)

	cases := []struct {
		pattern   string
		wantBase  string
		wantMatch string
	}{
		{pattern: "/*", wantBase: "/", wantMatch: "*"},
		{pattern: "/*.conf", wantBase: "/", wantMatch: "*.conf"},
		{pattern: "/*/*.conf", wantBase: "/", wantMatch: "*/*.conf"},
		{pattern: "C:/*.conf", wantBase: driveRoot, wantMatch: "*.conf"},
	}

	for _, tc := range cases {
		t.Run(tc.pattern, func(t *testing.T) {
			t.Parallel()
			base, match := splitPattern(tc.pattern)
			if base != tc.wantBase || match != tc.wantMatch {
				t.Errorf("splitPattern(%q) = (%q, %q), want (%q, %q)",
					tc.pattern, base, match, tc.wantBase, tc.wantMatch)
			}
		})
	}
}

// TestPathDepth covers the component counting the pattern matcher relies on. The
// separator that ends the base directory is not a component boundary, so a file
// directly inside the volume root is one level below it.
func TestPathDepth(t *testing.T) {
	t.Parallel()

	cases := []struct {
		baseDir string
		path    string
		want    int
	}{
		{baseDir: "/", path: "/", want: 0},
		{baseDir: "/", path: "/x.conf", want: 1},
		{baseDir: "/", path: "/sub/x.conf", want: 2},
		{baseDir: "/data", path: "/data", want: 0},
		{baseDir: "/data", path: "/data/x.conf", want: 1},
		{baseDir: "/data", path: "/data/sub/x.conf", want: 2},
		{baseDir: "/data/x", path: "/data/xyz", want: 0},
		{baseDir: "/", path: "relative.conf", want: 0},
	}

	for _, tc := range cases {
		t.Run(tc.baseDir+"|"+tc.path, func(t *testing.T) {
			t.Parallel()
			if got := pathDepth(tc.baseDir, tc.path); got != tc.want {
				t.Errorf("pathDepth(%q, %q) = %d, want %d", tc.baseDir, tc.path, got, tc.want)
			}
		})
	}
}

// TestMatchComponent_CaseFolding covers the platform rule for glob matching. On
// Windows, where filenames are case-insensitive, "*.TXT" is expected to find
// "photo.txt" exactly as the shell would; a case-sensitive platform keeps the two
// apart, because a case-insensitive volume is not detected at run time.
func TestMatchComponent_CaseFolding(t *testing.T) {
	t.Parallel()

	if foldCase {
		for _, tc := range []struct{ pattern, name string }{
			{pattern: "*.TXT", name: "photo.txt"},
			{pattern: "PHOTO.TXT", name: "photo.txt"},
			{pattern: "*.txt", name: "PHOTO.TXT"},
		} {
			if !matchComponent(tc.pattern, tc.name) {
				t.Errorf("matchComponent(%q, %q) = false, want true on a case-folding platform",
					tc.pattern, tc.name)
			}
		}
		return
	}

	if matchComponent("*.TXT", "photo.txt") {
		t.Error(`a case-sensitive platform must not match "*.TXT" against "photo.txt"`)
	}
}
