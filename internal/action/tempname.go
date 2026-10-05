package action

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	// tempNameAttempts bounds how often a temporary name is retried when another
	// process won the race for it.
	tempNameAttempts = 16

	// tempNamePrefix marks the temporary files this package creates next to a
	// victim; they either become the victim or are removed.
	tempNamePrefix = ".finddupe-"
)

// withTemporaryName calls create with a free name in dst's directory for a file
// that does not exist yet, then replaces dst with it.
//
// The obvious alternative — reserving a name with [os.CreateTemp] and removing the
// file before creating the real one — has a window in which another process can
// take the name, so the create fails and the cleanup removes *their* file. The
// name is therefore never reserved: create is retried on [fs.ErrExist], and
// nothing is left behind when it fails.
func withTemporaryName(dst string, create func(tmpPath string) error) error {
	dir := filepath.Dir(dst)

	for range tempNameAttempts {
		tmpPath := filepath.Join(dir, tempNamePrefix+randomSuffix())

		createErr := create(tmpPath)
		if errors.Is(createErr, fs.ErrExist) {
			continue
		}
		if createErr != nil {
			return createErr
		}

		if replaceErr := replaceFile(tmpPath, dst); replaceErr != nil {
			_ = os.Remove(tmpPath) // best effort: report the replacement failure
			return fmt.Errorf("replace %s: %w", dst, replaceErr)
		}
		return nil
	}
	return fmt.Errorf("no free temporary name next to %s", dst)
}

// randomSuffix returns a random hex string for a temporary name.
func randomSuffix() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// The name only has to be unlikely, not unguessable.
		return "fallback"
	}
	return hex.EncodeToString(buf[:])
}
