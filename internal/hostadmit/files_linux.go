//go:build linux

package hostadmit

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const dirFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC

// errNotThere marks a read path that does not exist or runs through a file.
var errNotThere = errors.New("not there")

// split returns the directories of an absolute path and its last element.
func split(path string) (dirs []string, base string, err error) {
	if !filepath.IsAbs(path) {
		return nil, "", fmt.Errorf("%s is not an absolute path", path)
	}
	clean := filepath.Clean(path)
	if clean == "/" {
		return nil, "", fmt.Errorf("%s is not a file", path)
	}
	parts := strings.Split(strings.TrimPrefix(clean, "/"), "/")
	return parts[:len(parts)-1], parts[len(parts)-1], nil
}

// openDirs opens "/" and then each directory in dirs, by descriptor relative
// to the one before it, never following a link. With create, a missing
// directory is made. The caller closes the descriptor returned.
func openDirs(dirs []string, create bool) (int, error) {
	fd, err := unix.Open("/", dirFlags, 0)
	if err != nil {
		return -1, err
	}
	walked := ""
	for _, name := range dirs {
		walked += "/" + name
		next, err := unix.Openat(fd, name, dirFlags, 0)
		if errors.Is(err, unix.ENOENT) && create {
			if err = unix.Mkdirat(fd, name, 0o755); err == nil || errors.Is(err, unix.EEXIST) {
				next, err = unix.Openat(fd, name, dirFlags, 0)
			}
		}
		if err != nil {
			var st unix.Stat_t
			isLink := unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW) == nil && st.Mode&unix.S_IFMT == unix.S_IFLNK
			unix.Close(fd)
			switch {
			case isLink:
				return -1, fmt.Errorf("%s is a symbolic link", walked)
			case errors.Is(err, unix.ENOENT), errors.Is(err, unix.ENOTDIR):
				return -1, fmt.Errorf("%s: %w", walked, errors.Join(errNotThere, err))
			}
			return -1, fmt.Errorf("%s: %w", walked, err)
		}
		unix.Close(fd)
		fd = next
	}
	return fd, nil
}

func readFile(path string) (string, bool, error) {
	dirs, base, err := split(path)
	if err != nil {
		return "", false, err
	}
	dirfd, err := openDirs(dirs, false)
	if errors.Is(err, errNotThere) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	defer unix.Close(dirfd)
	fd, err := unix.Openat(dirfd, base, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR) {
		return "", false, nil
	}
	if errors.Is(err, unix.ELOOP) {
		return "", false, fmt.Errorf("%s is a symbolic link", path)
	}
	if err != nil {
		return "", false, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return "", false, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return "", false, fmt.Errorf("%s is not a regular file", path)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return "", false, err
	}
	return string(data), true, nil
}

func writeFile(path, content string, mode uint32) error {
	dirs, base, err := split(path)
	if err != nil {
		return err
	}
	dirfd, err := openDirs(dirs, true)
	if err != nil {
		return err
	}
	defer unix.Close(dirfd)
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	tmp := "." + base + ".paisans-tmp-" + hex.EncodeToString(random[:])
	fd, err := unix.Openat(dirfd, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), tmp)
	fail := func(err error) error {
		f.Close()
		unix.Unlinkat(dirfd, tmp, 0)
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		return fail(err)
	}
	if err := unix.Fchmod(fd, mode&0o7777); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		unix.Unlinkat(dirfd, tmp, 0)
		return err
	}
	if err := unix.Renameat(dirfd, tmp, dirfd, base); err != nil {
		unix.Unlinkat(dirfd, tmp, 0)
		return err
	}
	return nil
}
