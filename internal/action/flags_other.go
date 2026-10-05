//go:build !darwin

package action

// preservePlatformFlags is a no-op on platforms without BSD file flags.
func preservePlatformFlags(_, _ string) error {
	return nil
}
