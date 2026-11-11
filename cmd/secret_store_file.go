//go:build linux

package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// The file store (SPEC.md, "Secrets"): where cpb's default helper keeps a
// cpb:<name> value on Linux when no Secret Service can hold it (no
// secret-tool, or no keyring daemon or session bus). One file per name,
// $XDG_DATA_HOME/cpb/secrets/<name> (default ~/.local/share/cpb/secrets),
// the directory 0700 and the file 0600, both owned by the user. The value
// is plaintext at rest: readable by that user and by root, and gone with a
// home that is thrown away. Nothing of cpb's playbook handling (export,
// copy, reset, drop) reaches this directory, which is outside every
// playbooks root.

// fileStoreMaxValue is the longest value the file store keeps: the Secret
// Service's limit, so a value fits whichever Linux store takes it, and the
// store step reads no longer a line for either.
const fileStoreMaxValue = 4096

// secretsDir is the file store's directory. A relative XDG_DATA_HOME is
// ignored, as the XDG rules say.
func secretsDir() (string, error) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" || !filepath.IsAbs(base) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("the file store needs a home directory: %w", err)
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "cpb", "secrets"), nil
}

// safeSecretFileName refuses a name that is not a plain file name. The
// cpb:<name> grammar already allows none of these; the file layer checks
// again, so no caller can reach a path outside the directory.
func safeSecretFileName(name string) error {
	if manifest.ValidateCPBRef(manifest.CPBRefPrefix+name) != nil || name == "" || name[0] == '.' {
		return errors.New("a cpb: reference's name is not a safe file name; nothing was read or written")
	}
	for _, c := range []byte(name) {
		if c < 0x21 || c > 0x7e || c == '/' || c == '\\' {
			return errors.New("a cpb: reference's name is not a safe file name; nothing was read or written")
		}
	}
	return nil
}

type fileStore struct{ dir string }

func (f fileStore) path(name string) string { return filepath.Join(f.dir, name) }

// ownedPrivate checks what Lstat found: not a link, the kind wanted, owned
// by this user, and no group or other permission.
func ownedPrivate(path string, info os.FileInfo, dir bool) error {
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("%s is a symbolic link, which the file store never follows; nothing was read or written", path)
	case dir && !info.IsDir(), !dir && !info.Mode().IsRegular():
		return fmt.Errorf("%s is not a %s; nothing was read or written", path, map[bool]string{true: "directory", false: "regular file"}[dir])
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s is owned by another user; nothing was read or written", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		want := map[bool]string{true: "0700", false: "0600"}[dir]
		return fmt.Errorf("%s is open to group or others (mode %04o, want %s); nothing was read or written. Fix it with chmod %s %s", path, info.Mode().Perm(), want, want, path)
	}
	return nil
}

// checkDir reports whether the directory is there, and refuses one that
// cannot be trusted.
func (f fileStore) checkDir() (bool, error) {
	info, err := os.Lstat(f.dir)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, ownedPrivate(f.dir, info, true)
}

func (f fileStore) exists(name string) (bool, error) {
	if err := safeSecretFileName(name); err != nil {
		return false, err
	}
	there, err := f.checkDir()
	if err != nil || !there {
		return false, err
	}
	info, err := os.Lstat(f.path(name))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := ownedPrivate(f.path(name), info, false); err != nil {
		return false, err
	}
	return true, nil
}

func (f fileStore) read(name string) ([]byte, error) {
	if err := safeSecretFileName(name); err != nil {
		return nil, err
	}
	if there, err := f.checkDir(); err != nil {
		return nil, err
	} else if !there {
		return nil, fmt.Errorf("cpb:%s is not in the file store", name)
	}
	p := f.path(name)
	fh, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("cpb:%s could not be read from the file store (%v)", name, err)
	}
	defer fh.Close()
	info, err := fh.Stat()
	if err != nil {
		return nil, err
	}
	if err := ownedPrivate(p, info, false); err != nil {
		return nil, err
	}
	v, err := io.ReadAll(io.LimitReader(fh, fileStoreMaxValue+1))
	if err != nil || len(v) > fileStoreMaxValue {
		clear(v)
		return nil, fmt.Errorf("cpb:%s could not be read from the file store", name)
	}
	return v, nil
}

// write creates the file and never replaces one: the value goes to a
// temporary file (0600, created exclusively) in the same directory, is
// synced, and is linked to its name, which fails when the name exists.
func (f fileStore) write(name string, value []byte) error {
	if err := safeSecretFileName(name); err != nil {
		return err
	}
	if len(value) > fileStoreMaxValue {
		return fmt.Errorf("the value is %d bytes, and the file store keeps at most %d; nothing was stored", len(value), fileStoreMaxValue)
	}
	if err := os.MkdirAll(f.dir, 0o700); err != nil {
		return fmt.Errorf("the file store %s could not be made (%v); nothing was stored", f.dir, err)
	}
	if _, err := f.checkDir(); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(f.dir, ".tmp-"+name+"-*")
	if err != nil {
		return fmt.Errorf("the file store could not take cpb:%s (%v); nothing was stored", name, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	_, werr := tmp.Write(value)
	if werr == nil {
		werr = tmp.Sync()
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return fmt.Errorf("the file store could not take cpb:%s (%v); nothing was stored", name, werr)
	}
	if err := os.Link(tmpName, f.path(name)); err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("cpb:%s is in the file store already, and cpb never replaces a stored secret: store the new value under a new name", name)
		}
		return fmt.Errorf("the file store could not take cpb:%s (%v); nothing was stored", name, err)
	}
	if d, err := os.Open(f.dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}
