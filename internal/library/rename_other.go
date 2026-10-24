//go:build !linux

package library

import (
	"errors"
	"os"
)

func renameNoReplace(root *os.Root, from, to string) error {
	return errors.New("recoverable file moves currently require Linux")
}
