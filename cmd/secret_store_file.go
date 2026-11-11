//go:build linux

package cmd

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"

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
//
// The directory is opened once, without following a link at it, after
// every directory above it is checked (owned by root or this user, and
// writable by no one else unless sticky, as /tmp is), and every file is
// then reached through that handle: nothing can be swapped in by another
// user between a check and its use.

// fileStoreMaxValue is the longest value the file store keeps: the Secret
// Service's limit, so a value fits whichever Linux store takes it, and the
// store step reads no longer a line for either.
const fileStoreMaxValue = 4096

// helperStoreDir is the file store's directory as the cpb that runs the
// helper resolved it (CPB_SECRET_STORE_DIR, read in helper mode only), so
// a launch environment that sets XDG_DATA_HOME or HOME never moves it.
var helperStoreDir string

// secretsDir is the file store's directory. A relative XDG_DATA_HOME is
// ignored, as the XDG rules say.
func secretsDir() (string, error) {
	if helperStoreDir != "" {
		return helperStoreDir, nil
	}
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

// setHelperStoreDir takes the directory the running cpb handed the helper.
func setHelperStoreDir(d string) {
	if filepath.IsAbs(d) {
		helperStoreDir = d
	}
}

// storeDirForHelper is the directory handed to the default helper.
func storeDirForHelper() string {
	d, err := secretsDir()
	if err != nil {
		return ""
	}
	return d
}

// safeSecretFileName refuses a name that is not a plain file name. The
// cpb:<name> grammar already allows none of these; the file layer checks
// again, so no caller can reach a path outside the directory.
func safeSecretFileName(name string) error {
	if manifest.ValidateCPBRef(manifest.CPBRefPrefix+name) != nil || name == "" || name[0] == '.' {
		return errors.New("a cpb: reference's name is not a safe file name; nothing was read or written")
	}
	for _, c := range []byte(name) {
		if c < 0x21 || c > 0x7e || c == '/' || c == '\\' || c == '~' {
			return errors.New("a cpb: reference's name is not a safe file name; nothing was read or written")
		}
	}
	return nil
}

type fileStore struct{ dir string }

func (f fileStore) path(name string) string { return filepath.Join(f.dir, name) }

// fileOwner is the uid that owns what st describes (a test replaces it).
var fileOwner = func(st *unix.Stat_t) int { return int(st.Uid) }

// ownedPrivate checks what fstat or fstatat found: not a link, the kind
// wanted, owned by this user, and no group or other permission. The line
// says what is wrong, and for an open mode the chmod that fixes it.
func ownedPrivate(path string, st *unix.Stat_t, dir bool) error {
	kind := st.Mode & unix.S_IFMT
	switch {
	case kind == unix.S_IFLNK:
		return fmt.Errorf("%s is a symbolic link, which the file store never follows; nothing was read or written. Remove the link", path)
	case dir && kind != unix.S_IFDIR, !dir && kind != unix.S_IFREG:
		return fmt.Errorf("%s is not a %s; nothing was read or written", path, map[bool]string{true: "directory", false: "regular file"}[dir])
	}
	if fileOwner(st) != os.Getuid() {
		return fmt.Errorf("%s is owned by another user (uid %d); nothing was read or written. Only a file store you own is used", path, fileOwner(st))
	}
	if perm := st.Mode & 0o777; perm&0o077 != 0 {
		want := map[bool]string{true: "0700", false: "0600"}[dir]
		return fmt.Errorf("%s is open to group or others (mode %04o, want %s); nothing was read or written. Fix it with chmod %s %s", path, perm, want, want, path)
	}
	return nil
}

// privateGroup is the user's own group where the system gives each user one
// (a group named as the user that is the user's primary group, as Debian
// and Ubuntu do, with umask 002): its write bit lets no one else in. -1
// when there is none, or it cannot be told (a test replaces it).
var privateGroup = sync.OnceValue(func() int {
	u, err := user.Current()
	if err != nil {
		return -1
	}
	g, err := user.LookupGroupId(u.Gid)
	if err != nil || g.Name != u.Username {
		return -1
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return -1
	}
	return gid
})

// ancestorWritableByOthers reports whether a directory st describes lets
// someone other than its owner change its entries: writable by others, or
// by a group that is not the user's own private group, without the sticky
// bit (/tmp's keeps others from renaming what this user owns in it).
func ancestorWritableByOthers(st *unix.Stat_t) bool {
	if st.Mode&unix.S_ISVTX != 0 {
		return false
	}
	return st.Mode&0o002 != 0 || (st.Mode&0o020 != 0 && int(st.Gid) != privateGroup())
}

// checkAncestors refuses a directory above the store that another user
// could change: one owned by anyone but root or this user, or writable by
// anyone else (ancestorWritableByOthers). p is resolved first, so a link
// among the ancestors (a /home that points elsewhere) is judged by where
// it leads.
func checkAncestors(p string) error {
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return fmt.Errorf("the file store's directory %s could not be resolved (%v); nothing was read or written", p, err)
	}
	for d := real; ; d = filepath.Dir(d) {
		var st unix.Stat_t
		if err := unix.Lstat(d, &st); err != nil {
			return fmt.Errorf("%s could not be checked (%v); nothing was read or written", d, err)
		}
		if owner := fileOwner(&st); owner != 0 && owner != os.Getuid() {
			return fmt.Errorf("%s, above the file store, is owned by another user (uid %d); nothing was read or written. Keep the file store under directories only you or root own", d, owner)
		}
		if ancestorWritableByOthers(&st) {
			return fmt.Errorf("%s, above the file store, is writable by others or by a shared group (mode %04o), so it could be swapped; nothing was read or written. Keep the file store under directories only you can write", d, st.Mode&0o7777)
		}
		if d == "/" || d == "." {
			return nil
		}
	}
}

// openDir opens the store's directory, never through a link at it, after
// its ancestors are checked; with create it makes the directory first
// (0700, and any missing parent 0700). It returns nil, nil when the
// directory is not there and create is false.
func (f fileStore) openDir(create bool) (*os.File, error) {
	if create {
		if err := os.MkdirAll(f.dir, 0o700); err != nil {
			return nil, fmt.Errorf("the file store %s could not be made (%v); nothing was stored", f.dir, err)
		}
	} else if _, err := os.Lstat(f.dir); os.IsNotExist(err) {
		return nil, nil
	}
	if err := checkAncestors(filepath.Dir(f.dir)); err != nil {
		return nil, err
	}
	fd, err := unix.Open(f.dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		var st unix.Stat_t
		if unix.Lstat(f.dir, &st) == nil {
			if perr := ownedPrivate(f.dir, &st, true); perr != nil {
				return nil, perr
			}
		}
		return nil, fmt.Errorf("the file store %s could not be opened (%v); nothing was read or written", f.dir, err)
	}
	d := os.NewFile(uintptr(fd), f.dir)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		d.Close()
		return nil, err
	}
	if err := ownedPrivate(f.dir, &st, true); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

// statAt is name in the open directory d, not followed if it is a link;
// found is false when it is not there.
func statAt(d *os.File, name string) (st unix.Stat_t, found bool, err error) {
	err = unix.Fstatat(int(d.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return st, false, nil
	}
	return st, err == nil, err
}

func (f fileStore) exists(name string) (bool, error) {
	if err := safeSecretFileName(name); err != nil {
		return false, err
	}
	d, err := f.openDir(false)
	if err != nil || d == nil {
		return false, err
	}
	defer d.Close()
	st, found, err := statAt(d, name)
	if err != nil || !found {
		return false, err
	}
	if err := ownedPrivate(f.path(name), &st, false); err != nil {
		return false, err
	}
	return true, nil
}

// fileValueProblem says why a value read from the file store is not one
// --store would have written, or "": empty, a line end at its end, a byte
// outside printable ASCII, or too long. A file made by hand with a newline
// at its end (an editor, echo) would otherwise hand every launch the value
// with that newline.
func fileValueProblem(v []byte) string {
	switch {
	case len(v) == 0:
		return "it is empty"
	case len(v) > fileStoreMaxValue:
		return fmt.Sprintf("it is longer than %d bytes", fileStoreMaxValue)
	case v[len(v)-1] == '\n' || v[len(v)-1] == '\r':
		return "it ends with a line end (write the value with printf '%s', not echo or an editor)"
	}
	for _, c := range v {
		if c < 0x20 || c > 0x7e {
			return "it holds a byte outside printable ASCII (a tab, a line end, or a non-ASCII character)"
		}
	}
	return ""
}

func (f fileStore) read(name string) ([]byte, error) {
	if err := safeSecretFileName(name); err != nil {
		return nil, err
	}
	d, err := f.openDir(false)
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, fmt.Errorf("cpb:%s is not in the file store", name)
	}
	defer d.Close()
	p := f.path(name)
	fd, err := unix.Openat(int(d.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, fmt.Errorf("%s is a symbolic link, which the file store never follows; nothing was read. Remove the link", p)
		}
		return nil, fmt.Errorf("cpb:%s could not be read from the file store (%v)", name, err)
	}
	fh := os.NewFile(uintptr(fd), p)
	defer fh.Close()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return nil, err
	}
	if err := ownedPrivate(p, &st, false); err != nil {
		return nil, err
	}
	v, err := io.ReadAll(io.LimitReader(fh, fileStoreMaxValue+1))
	if err != nil {
		clear(v)
		return nil, fmt.Errorf("cpb:%s could not be read from the file store (%v)", name, err)
	}
	if why := fileValueProblem(v); why != "" {
		clear(v)
		return nil, fmt.Errorf("the file %s does not hold a value cpb would have stored: %s; nothing was read. Store it with cpb play, or write the value alone into the file", p, why)
	}
	return v, nil
}

// tempPrefix is the prefix of name's temporary files. "~" is in no name,
// so one name's prefix never matches another name's temporary files.
func tempPrefix(name string) string { return ".tmp-" + name + "~" }

// sweepTemps removes temporary files of name that a write killed before
// it could remove its own: each may hold the value, and after the link it
// is a second name for the stored file. Called under the per-name lock,
// so no write of name is running.
func sweepTemps(d *os.File, name string) {
	names, err := d.Readdirnames(-1)
	if err != nil {
		return
	}
	for _, n := range names {
		if strings.HasPrefix(n, tempPrefix(name)) {
			unix.Unlinkat(int(d.Fd()), n, 0)
		}
	}
}

// write creates the file and never replaces one: the value goes to a
// temporary file (0600, created exclusively) in the same directory, is
// synced, and is linked to its name, which fails when the name exists, a
// dangling link included. Everything goes through the directory's handle.
func (f fileStore) write(name string, value []byte) error {
	if err := safeSecretFileName(name); err != nil {
		return err
	}
	if len(value) > fileStoreMaxValue {
		return fmt.Errorf("the value is %d bytes, and the file store keeps at most %d; nothing was stored", len(value), fileStoreMaxValue)
	}
	d, err := f.openDir(true)
	if err != nil {
		return err
	}
	defer d.Close()
	dfd := int(d.Fd())
	sweepTemps(d, name)
	var rnd [8]byte
	rand.Read(rnd[:])
	tmp := tempPrefix(name) + hex.EncodeToString(rnd[:])
	fd, err := unix.Openat(dfd, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return fmt.Errorf("the file store could not take cpb:%s (%v); nothing was stored", name, err)
	}
	defer unix.Unlinkat(dfd, tmp, 0)
	th := os.NewFile(uintptr(fd), tmp)
	_, werr := th.Write(value)
	if werr == nil {
		werr = th.Sync()
	}
	if cerr := th.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return fmt.Errorf("the file store could not take cpb:%s (%v); nothing was stored", name, werr)
	}
	if err := unix.Linkat(dfd, tmp, dfd, name, 0); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return fmt.Errorf("cpb:%s is in the file store already (or its name is taken by a link), and cpb never replaces a stored secret: store the new value under a new name", name)
		}
		return fmt.Errorf("the file store could not take cpb:%s (%v); nothing was stored", name, err)
	}
	unix.Unlinkat(dfd, tmp, 0)
	unix.Fsync(dfd)
	return nil
}

// ready reports whether the file store could take a value now: its
// directory can be made (or opened) and trusted, and written. Nothing is
// written.
func (f fileStore) ready() error {
	if _, err := os.Lstat(f.dir); os.IsNotExist(err) {
		// The nearest existing ancestor must be trusted and writable.
		p := filepath.Dir(f.dir)
		for {
			if _, err := os.Lstat(p); err == nil || filepath.Dir(p) == p {
				break
			}
			p = filepath.Dir(p)
		}
		if err := checkAncestors(p); err != nil {
			return err
		}
		if err := unix.Access(p, unix.W_OK|unix.X_OK); err != nil {
			return fmt.Errorf("the file store %s cannot be made: %s is not writable (%v); nothing was asked for or stored", f.dir, p, err)
		}
		return nil
	}
	d, err := f.openDir(false)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := unix.Access(f.dir, unix.W_OK|unix.X_OK); err != nil {
		return fmt.Errorf("the file store %s is not writable (%v); nothing was asked for or stored", f.dir, err)
	}
	return nil
}
