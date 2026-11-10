// Package diskguard keeps the machine's disks from filling up: it reads how
// full the disks cmdr writes to are (warn at 80%, refuse new Operators at 95%)
// and owns the per-run scratch root Operators get as TMPDIR, which is removed
// when a run ends and swept by age.
package diskguard

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Default thresholds (percent of the disk used). Env COMMANDER_DISK_WARN_PCT and
// COMMANDER_DISK_REFUSE_PCT override them (1-100), for tests and for a machine
// that wants a different line.
const (
	DefaultWarnPct   = 80
	DefaultRefusePct = 95
)

// Usage is how full the filesystem holding Path is.
type Usage struct {
	Path    string  // the path asked about
	Mount   string  // the mount point holding it (best effort: the topmost dir on the same device)
	Dev     uint64  // device id, to dedupe paths on one disk
	UsedPct float64 // 0-100, as df reports it (used / (used + available))
	Free    uint64  // bytes available to an unprivileged user
	Total   uint64  // bytes
}

// Stat reads one path's disk. A var so tests can fake a full disk.
var Stat = statPath

func statPath(path string) (Usage, error) {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(path, &fs); err != nil {
		return Usage{}, err
	}
	bsize := uint64(fs.Bsize)
	used := (uint64(fs.Blocks) - uint64(fs.Bfree)) * bsize
	avail := uint64(fs.Bavail) * bsize
	u := Usage{Path: path, Free: avail, Total: uint64(fs.Blocks) * bsize}
	if used+avail > 0 {
		u.UsedPct = float64(used) * 100 / float64(used+avail)
	}
	u.Dev, u.Mount = mountOf(path)
	return u, nil
}

// mountOf walks up from path while the device stays the same.
func mountOf(path string) (uint64, string) {
	p, err := filepath.Abs(path)
	if err != nil {
		return 0, path
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	dev, ok := devOf(p)
	if !ok {
		return 0, p
	}
	for {
		parent := filepath.Dir(p)
		if parent == p {
			return dev, p
		}
		if d, ok := devOf(parent); !ok || d != dev {
			return dev, p
		}
		p = parent
	}
}

func devOf(p string) (uint64, bool) {
	var st syscall.Stat_t
	if err := syscall.Stat(p, &st); err != nil {
		return 0, false
	}
	return uint64(st.Dev), true //nolint:unconvert // int32 on darwin
}

// WarnPct and RefusePct are the thresholds in force.
func WarnPct() float64   { return pct("COMMANDER_DISK_WARN_PCT", DefaultWarnPct) }
func RefusePct() float64 { return pct("COMMANDER_DISK_REFUSE_PCT", DefaultRefusePct) }

func pct(env string, def float64) float64 {
	if v, err := strconv.ParseFloat(os.Getenv(env), 64); err == nil && v > 0 && v <= 100 {
		return v
	}
	return def
}

// Disks reads every distinct disk under paths (missing or unreadable paths are
// skipped), fullest first.
func Disks(paths ...string) []Usage {
	seen := map[uint64]bool{}
	var out []Usage
	for _, p := range paths {
		if p == "" {
			continue
		}
		u, err := Stat(p)
		if err != nil {
			continue
		}
		if u.Dev != 0 {
			if seen[u.Dev] {
				continue
			}
			seen[u.Dev] = true
		}
		out = append(out, u)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UsedPct > out[j].UsedPct })
	return out
}

// Describe is one line naming the disk: "disk / is 96% full (3.1 GB free)".
func (u Usage) Describe() string {
	return fmt.Sprintf("disk %s is %.0f%% full (%s free)", u.Mount, u.UsedPct, HumanBytes(u.Free))
}

// HumanBytes formats n as "3.1 GB".
func HumanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// RefuseError is a spawn refused because a disk it writes to is too full.
type RefuseError struct{ Disk Usage }

func (e *RefuseError) Error() string {
	return fmt.Sprintf("%s (at or above %.0f%%): new Operators are refused until it drops; free space first (cmdr worktrees gc, old build caches), then retry", e.Disk.Describe(), RefusePct())
}

// CheckSpawn refuses (a *RefuseError) when any disk under paths is at or above
// RefusePct. An unreadable disk passes: a failed statfs must not stop the fleet.
func CheckSpawn(paths ...string) error {
	limit := RefusePct()
	for _, u := range Disks(paths...) {
		if u.UsedPct >= limit {
			return &RefuseError{Disk: u}
		}
	}
	return nil
}

// TmpRoot is the one root of Operators' scratch: $COMMANDER_HOME/tmp. Nothing
// outside it is ever removed by this package.
func TmpRoot(commanderHome string) string { return filepath.Join(commanderHome, "tmp") }

// RunTmpDir is a run's TMPDIR, keyed by its tmux session name. Short on
// purpose: tools put unix sockets under TMPDIR, and those paths have a ~104
// byte limit.
func RunTmpDir(commanderHome, sessionName string) string {
	return filepath.Join(TmpRoot(commanderHome), RunTmpName(sessionName))
}

// RunTmpName is the entry name RunTmpDir uses under TmpRoot.
func RunTmpName(sessionName string) string {
	sum := sha1.Sum([]byte(sessionName))
	return "op-" + hex.EncodeToString(sum[:])[:10]
}

// SweepTmp removes the leftovers under root: every real directory named like
// RunTmpName (op-<10 hex>), not named in keep (a live run) and untouched for
// olderThan. Anything else there (a file an agent left, a link, another dir) is
// never touched: only what this package creates goes. It refuses a root that
// is not a directory named "tmp". It returns the entries removed.
func SweepTmp(root string, keep map[string]bool, olderThan time.Duration, now time.Time) ([]string, error) {
	if err := checkRoot(root); err != nil {
		return nil, err
	}
	info, err := os.Lstat(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("diskguard: %s is not a directory", root)
	}
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, e := range ents {
		if keep[e.Name()] || !IsRunTmpName(e.Name()) {
			continue
		}
		p := filepath.Join(root, e.Name())
		fi, err := os.Lstat(p)
		if err != nil || !fi.IsDir() || now.Sub(fi.ModTime()) < olderThan {
			continue
		}
		if err := removeAll(p); err != nil {
			continue
		}
		removed = append(removed, e.Name())
	}
	return removed, nil
}

// IsRunTmpName reports whether name is exactly what RunTmpName makes.
func IsRunTmpName(name string) bool {
	if len(name) != 13 || !strings.HasPrefix(name, "op-") {
		return false
	}
	for _, c := range name[3:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// checkRoot refuses any root but an exact, clean <dir>/tmp below a real
// directory: never "/tmp", "/tmp/", the system temp dir or anything resolving
// to them (incident: "/tmp/" once passed a Base/Dir check and a test swept the
// machine's /tmp).
func checkRoot(root string) error {
	bad := func() error {
		return fmt.Errorf("diskguard: refusing to sweep %q: not a <commander home>/tmp root", root)
	}
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root || filepath.Base(root) != "tmp" {
		return bad()
	}
	parent := filepath.Dir(root)
	if parent == "/" || filepath.Dir(parent) == parent {
		return bad()
	}
	if fi, err := os.Lstat(root); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return bad() // a root that is a link sweeps wherever it points
	}
	system := map[string]bool{"/tmp": true, "/private/tmp": true, "/var/tmp": true, filepath.Clean(os.TempDir()): true}
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		// COMMANDER_HOME=$HOME would make ~/tmp the root: the user's own folder.
		system[filepath.Clean(h)] = true
		if r, err := filepath.EvalSymlinks(h); err == nil {
			system[r] = true
		}
	}
	for _, p := range []string{root, parent} {
		real := p
		if r, err := filepath.EvalSymlinks(p); err == nil {
			real = r
		}
		if system[p] || system[real] || system[filepath.Clean(real)] {
			return bad()
		}
	}
	return nil
}

// removeAll is os.RemoveAll that first makes directories writable (Go's module
// cache and test caches write read-only trees, which RemoveAll cannot empty).
func removeAll(p string) error {
	if err := os.RemoveAll(p); err == nil {
		return nil
	}
	_ = filepath.WalkDir(p, func(path string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(path, 0o700)
		}
		return nil
	})
	return os.RemoveAll(p)
}
