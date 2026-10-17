//go:build linux || darwin

package nodeagent

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

func checkExecutable(path string) error {
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0022 != 0 || st.Mode().Perm()&0111 == 0 {
		return errors.New("unsafe executable")
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !ok || (owner.Uid != 0 && int(owner.Uid) != os.Geteuid()) {
		return errors.New("unsafe executable owner")
	}
	return nil
}
func checkRuntimeDir(path string) error {
	st, err := os.Lstat(path)
	if !filepath.IsAbs(path) || err != nil || !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return errors.New("private runtime directory required")
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !ok || int(owner.Uid) != os.Geteuid() {
		return errors.New("runtime directory must belong to process user")
	}
	return nil
}

func privateFile(path string, flags int) (*os.File, error) {
	fd, err := syscall.Open(path, flags|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || !ok || int(owner.Uid) != os.Geteuid() {
		f.Close()
		return nil, errors.New("file must be private, regular and owned by this process user")
	}
	return f, nil
}
func lockState(path string) (*os.File, error) {
	f, err := privateFile(path, syscall.O_CREAT|syscall.O_RDWR)
	if err != nil {
		return nil, err
	}
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		f.Close()
		return nil, errors.New("another agent owns this state directory")
	}
	return f, nil
}
func readPrivate(path string, limit int64) ([]byte, error) {
	f, err := privateFile(path, syscall.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, errors.New("file exceeds limit or cannot be read")
	}
	return b, nil
}
