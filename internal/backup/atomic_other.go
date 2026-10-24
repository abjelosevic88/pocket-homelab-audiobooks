//go:build !linux

package backup

import (
	"errors"
	"os"
)

func Exclusive(string) (*os.File, error) {
	return nil, errors.New("exclusive application operations require Linux")
}
func publishDirectory(string, string) error {
	return errors.New("atomic directory restoration requires Linux")
}
