//go:build !linux && !darwin

package application

import "errors"

func renameNoReplace(string, string) error {
	return errors.New("exclusive pending marker rename is unsupported on this platform")
}
