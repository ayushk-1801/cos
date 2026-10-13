//go:build darwin

package sysinfo

import (
	"bufio"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Pressure string

const (
	PressureNormal   Pressure = "normal"
	PressureElevated Pressure = "elevated"
	PressureCritical Pressure = "critical"
)

type Memory struct {
	TotalBytes, AvailableBytes uint64
	Pressure                   Pressure
}
type Process struct {
	PID, PPID                    int
	Command, Kind, Label         string
	PSSBytes, RSSBytes, CPUTicks uint64
	CPU                          float64
}
type Group struct {
	Kind               string
	Count              int
	PSSBytes, RSSBytes uint64
	CPU                float64
}
type Snapshot struct {
	At                 time.Time
	RootPID            int
	Processes          []Process
	Groups             []Group
	TotalPSS, TotalRSS uint64
	TotalCPU           float64
	Memory             Memory
}
type Sampler struct{ mu sync.Mutex }

func NewSampler() *Sampler { return &Sampler{} }

var pageSizeRE = regexp.MustCompile(`page size of ([0-9]+) bytes`)

func MemoryStatus() Memory {
	result := Memory{Pressure: PressureNormal}
	out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
	if err == nil {
		result.TotalBytes, _ = strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	}
	out, err = exec.Command("vm_stat").Output()
	if err != nil {
		return result
	}
	pageSize := uint64(4096)
	if match := pageSizeRE.FindSubmatch(out); len(match) == 2 {
		if n, e := strconv.ParseUint(string(match[1]), 10, 64); e == nil && n > 0 {
			pageSize = n
		}
	}
	var pages uint64
	scan := bufio.NewScanner(strings.NewReader(string(out)))
	for scan.Scan() {
		key, value, ok := strings.Cut(scan.Text(), ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key != "Pages free" && key != "Pages inactive" && key != "Pages speculative" && key != "Pages purgeable" {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(value), ".")), 10, 64)
		if err == nil {
			pages += n
		}
	}
	result.AvailableBytes = pages * pageSize
	if result.TotalBytes > 0 && result.AvailableBytes > result.TotalBytes {
		result.AvailableBytes = result.TotalBytes
	}
	if result.TotalBytes > 0 {
		ratio := float64(result.AvailableBytes) / float64(result.TotalBytes)
		if ratio < 0.10 {
			result.Pressure = PressureCritical
		} else if ratio < 0.20 {
			result.Pressure = PressureElevated
		}
	}
	return result
}

func allProcesses() []Process {
	out, err := exec.Command("ps", "-axo", "pid=,ppid=,rss=,%cpu=,command=").Output()
	if err != nil {
		return nil
	}
	var result []Process
	scan := bufio.NewScanner(strings.NewReader(string(out)))
	scan.Buffer(make([]byte, 4096), 1024*1024)
	for scan.Scan() {
		parts := strings.Fields(scan.Text())
		if len(parts) < 5 {
			continue
		}
		pid, e1 := strconv.Atoi(parts[0])
		ppid, e2 := strconv.Atoi(parts[1])
		rss, e3 := strconv.ParseUint(parts[2], 10, 64)
		cpu, e4 := strconv.ParseFloat(parts[3], 64)
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
			continue
		}
		command := strings.Join(parts[4:], " ")
		kind, label := classify(command)
		// Darwin does not expose Linux PSS. Treat PSSBytes as an RSS-based estimate,
		// preserving the existing API and making adaptive LSP eviction conservative.
		result = append(result, Process{PID: pid, PPID: ppid, Command: command, Kind: kind, Label: label, RSSBytes: rss * 1024, PSSBytes: rss * 1024, CPU: cpu})
	}
	return result
}

func classify(command string) (string, string) {
	lower := strings.ToLower(command)
	name := filepathBase(command)
	switch {
	case strings.Contains(lower, "cos daemon"):
		return "daemon", "cos-lite"
	case strings.Contains(lower, "tunnel-client run"):
		return "tunnel", "openai"
	case strings.Contains(lower, "/chrome") || strings.Contains(lower, "chromium"):
		return "browser", "chromium"
	case strings.Contains(lower, "gopls") || strings.Contains(lower, "clangd") || strings.Contains(lower, "rust-analyzer") || strings.Contains(lower, "pyright") || strings.Contains(lower, "typescript-language-server"):
		return "lsp", name
	case strings.Contains(lower, "bash") || strings.Contains(lower, "zsh"):
		return "shell", name
	default:
		return "other", name
	}
}
func filepathBase(command string) string {
	words := strings.Fields(command)
	if len(words) == 0 {
		return ""
	}
	name := words[0]
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		return name[i+1:]
	}
	return name
}

func descendants(root int, all []Process) []Process {
	byParent := map[int][]Process{}
	byPID := map[int]Process{}
	for _, p := range all {
		byParent[p.PPID] = append(byParent[p.PPID], p)
		byPID[p.PID] = p
	}
	if _, ok := byPID[root]; !ok {
		return nil
	}
	visited := map[int]bool{}
	queue := []int{root}
	var result []Process
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if visited[pid] {
			continue
		}
		visited[pid] = true
		if p, ok := byPID[pid]; ok {
			result = append(result, p)
		}
		for _, child := range byParent[pid] {
			queue = append(queue, child.PID)
		}
	}
	return result
}

func TreePSS(pid int) uint64 {
	var total uint64
	for _, p := range descendants(pid, allProcesses()) {
		total += p.RSSBytes
	}
	return total
}

func (s *Sampler) Sample(rootPID int) Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	procs := descendants(rootPID, allProcesses())
	sort.Slice(procs, func(i, j int) bool { return procs[i].RSSBytes > procs[j].RSSBytes })
	snap := Snapshot{At: time.Now(), RootPID: rootPID, Processes: procs, Memory: MemoryStatus()}
	groups := map[string]*Group{}
	for _, p := range procs {
		snap.TotalPSS += p.PSSBytes
		snap.TotalRSS += p.RSSBytes
		snap.TotalCPU += p.CPU
		group := groups[p.Kind]
		if group == nil {
			group = &Group{Kind: p.Kind}
			groups[p.Kind] = group
		}
		group.Count++
		group.PSSBytes += p.PSSBytes
		group.RSSBytes += p.RSSBytes
		group.CPU += p.CPU
	}
	for _, g := range groups {
		snap.Groups = append(snap.Groups, *g)
	}
	sort.Slice(snap.Groups, func(i, j int) bool { return snap.Groups[i].RSSBytes > snap.Groups[j].RSSBytes })
	return snap
}

func Bytes(v uint64) string {
	switch {
	case v >= 1<<30:
		return fmt.Sprintf("%.2f GiB", float64(v)/(1<<30))
	case v >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(v)/(1<<20))
	case v >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(v)/(1<<10))
	default:
		return fmt.Sprintf("%d B", v)
	}
}
