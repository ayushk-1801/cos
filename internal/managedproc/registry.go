package managedproc

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ayush/cos-lite/internal/control"
)

const registryVersion = 1

type Entry struct {
	PID       int    `json:"pid"`
	PGID      int    `json:"pgid,omitempty"`
	OwnerPID  int    `json:"owner_pid"`
	Kind      string `json:"kind"`
	Label     string `json:"label,omitempty"`
	StartedAt string `json:"started_at"`
}

type registryFile struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

type CleanupReport struct {
	Killed  []Entry
	Pruned  []Entry
	Foreign []Entry
	Legacy  []int
}

var mu sync.Mutex

func Path() (string, error) {
	d, err := control.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "managed-processes.json"), nil
}

func ManagedEnv(base []string, kind, label string, ownerPID int) []string {
	out := append([]string(nil), base...)
	out = setEnv(out, "COS_LITE_MANAGED", strings.TrimSpace(kind))
	out = setEnv(out, "COS_LITE_MANAGED_LABEL", strings.TrimSpace(label))
	out = setEnv(out, "COS_LITE_OWNER_PID", strconv.Itoa(ownerPID))
	return out
}

func Register(pid, ownerPID int, kind, label string) error {
	if implicitTestState() {
		return nil
	}
	if pid <= 0 || ownerPID <= 0 || strings.TrimSpace(kind) == "" {
		return fmt.Errorf("invalid managed process registration")
	}
	pgid, _ := syscall.Getpgid(pid)
	entry := Entry{
		PID: pid, PGID: pgid, OwnerPID: ownerPID, Kind: strings.TrimSpace(kind),
		Label: strings.TrimSpace(label), StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	mu.Lock()
	defer mu.Unlock()
	f, _ := loadLocked()
	filtered := f.Entries[:0]
	for _, old := range f.Entries {
		if old.PID != pid && alive(old.PID) {
			filtered = append(filtered, old)
		}
	}
	f.Entries = append(filtered, entry)
	return saveLocked(f)
}

func Unregister(pid int) {
	if implicitTestState() {
		return
	}
	if pid <= 0 {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	f, err := loadLocked()
	if err != nil {
		return
	}
	out := f.Entries[:0]
	for _, e := range f.Entries {
		if e.PID != pid && alive(e.PID) {
			out = append(out, e)
		}
	}
	f.Entries = out
	_ = saveLocked(f)
}

func List() ([]Entry, error) {
	if implicitTestState() {
		return nil, nil
	}
	mu.Lock()
	defer mu.Unlock()
	f, err := loadLocked()
	if err != nil {
		return nil, err
	}
	return append([]Entry(nil), f.Entries...), nil
}

func CleanupStale(currentOwner int) (CleanupReport, error) {
	var report CleanupReport
	if implicitTestState() {
		return report, nil
	}
	mu.Lock()
	defer mu.Unlock()
	f, err := loadLocked()
	if err != nil {
		return report, err
	}
	keep := make([]Entry, 0, len(f.Entries))
	for _, e := range f.Entries {
		if !alive(e.PID) {
			report.Pruned = append(report.Pruned, e)
			continue
		}
		if e.OwnerPID == currentOwner {
			keep = append(keep, e)
			continue
		}
		if alive(e.OwnerPID) {
			report.Foreign = append(report.Foreign, e)
			keep = append(keep, e)
			continue
		}
		killEntry(e)
		report.Killed = append(report.Killed, e)
	}
	f.Entries = keep
	if err := saveLocked(f); err != nil {
		return report, err
	}
	report.Legacy = cleanupLegacyBrowsers(currentOwner)
	return report, nil
}

func StaleEntries(currentOwner int) ([]Entry, error) {
	if implicitTestState() {
		return nil, nil
	}
	entries, err := List()
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, e := range entries {
		if alive(e.PID) && e.OwnerPID != currentOwner && !alive(e.OwnerPID) {
			out = append(out, e)
		}
	}
	return out, nil
}

func loadLocked() (registryFile, error) {
	p, err := Path()
	if err != nil {
		return registryFile{}, err
	}
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return registryFile{Version: registryVersion}, nil
	}
	if err != nil {
		return registryFile{}, err
	}
	var f registryFile
	if err := json.Unmarshal(b, &f); err != nil {
		return registryFile{}, err
	}
	if f.Version == 0 {
		f.Version = registryVersion
	}
	return f, nil
}

func saveLocked(f registryFile) error {
	if err := control.EnsureDir(); err != nil {
		return err
	}
	p, err := Path()
	if err != nil {
		return err
	}
	f.Version = registryVersion
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, p)
}

func killEntry(e Entry) {
	if e.PGID > 1 {
		_ = syscall.Kill(-e.PGID, syscall.SIGKILL)
	} else if p, err := os.FindProcess(e.PID); err == nil {
		_ = p.Kill()
	}
}

func cleanupLegacyBrowsers(currentOwner int) []int {
	cache, err := os.UserCacheDir()
	if err != nil {
		return nil
	}
	marker := filepath.Join(cache, "cos-lite", "chromium-profiles") + string(filepath.Separator)
	procEntries, _ := os.ReadDir("/proc")
	var killed []int
	for _, de := range procEntries {
		pid, err := strconv.Atoi(de.Name())
		if err != nil || pid <= 1 || pid == currentOwner {
			continue
		}
		cmd, err := os.ReadFile(filepath.Join("/proc", de.Name(), "cmdline"))
		if err != nil || !strings.Contains(strings.ReplaceAll(string(cmd), "\x00", " "), marker) {
			continue
		}
		ppid, err := processPPID(pid)
		if err != nil || (ppid != 1 && ppid != 0) {
			continue
		}
		if pgid, err := syscall.Getpgid(pid); err == nil && pgid > 1 {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			killed = append(killed, pid)
		}
	}
	return killed
}

func processPPID(pid int) (int, error) {
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, err
	}
	s := string(b)
	i := strings.LastIndex(s, ")")
	if i < 0 || i+2 >= len(s) {
		return 0, errors.New("invalid stat")
	}
	fields := strings.Fields(s[i+2:])
	if len(fields) < 2 {
		return 0, errors.New("short stat")
	}
	return strconv.Atoi(fields[1])
}

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	return err == nil && p.Signal(syscall.Signal(0)) == nil
}

func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

func implicitTestState() bool {
	return strings.HasSuffix(filepath.Base(os.Args[0]), ".test") && os.Getenv("COS_LITE_TEST_MANAGED_REGISTRY") != "1"
}
