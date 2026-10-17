//go:build !linux && !darwin

package nodeagent

import (
	"errors"
	"os"
)

func lockState(string) (*os.File, error) { return nil, errors.New("unsupported node agent platform") }
func checkExecutable(string) error       { return errors.New("unsupported node agent platform") }
func checkRuntimeDir(string) error       { return errors.New("unsupported node agent platform") }
func readPrivate(string, int64) ([]byte, error) {
	return nil, errors.New("unsupported node agent platform")
}
