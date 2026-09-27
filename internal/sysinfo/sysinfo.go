package sysinfo

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
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
	TotalBytes     uint64
	AvailableBytes uint64
	Pressure       Pressure
}

type Process struct {
	PID      int
	PPID     int
	Command  string
	Kind     string
	Label    string
	PSSBytes uint64
	RSSBytes uint64
	CPUTicks uint64
	CPU      float64
}

type Group struct {
	Kind     string
	Count    int
	PSSBytes uint64
	RSSBytes uint64
	CPU      float64
}

type Snapshot struct {
	At        time.Time
	RootPID   int
	Processes []Process
	Groups    []Group
	TotalPSS  uint64
	TotalRSS  uint64
	TotalCPU  float64
	Memory    Memory
}

type Sampler struct {
	mu       sync.Mutex
	lastAt   time.Time
	lastTick map[int]uint64
	hz       float64
}

func NewSampler() *Sampler {
	return &Sampler{lastTick: map[int]uint64{}, hz: clockTicks()}
}

func MemoryStatus() Memory {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return Memory{Pressure: PressureNormal}
	}
	defer f.Close()
	var total, avail uint64
	s := bufio.NewScanner(f)
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) < 2 {
			continue
		}
		v, _ := strconv.ParseUint(fields[1], 10, 64)
		switch strings.TrimSuffix(fields[0], ":") {
		case "MemTotal":
			total = v * 1024
		case "MemAvailable":
			avail = v * 1024
		}
	}
	level := PressureNormal
	if total > 0 {
		ratio := float64(avail) / float64(total)
		if ratio < 0.10 || avail < 1<<30 {
			level = PressureCritical
		} else if ratio < 0.20 || avail < 2<<30 {
			level = PressureElevated
		}
	}
	return Memory{TotalBytes: total, AvailableBytes: avail, Pressure: level}
}

func TreePSS(pid int) uint64 {
	var total uint64
	for _, p := range processTree(pid) {
		total += processPSS(p)
	}
	return total
}

func (s *Sampler) Sample(rootPID int) Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	dt := now.Sub(s.lastAt).Seconds()
	procs := make([]Process, 0)
	next := map[int]uint64{}
	for _, pid := range processTree(rootPID) {
		p, ok := readProcess(pid)
		if !ok {
			continue
		}
		next[pid] = p.CPUTicks
		if dt > 0 && s.lastAt.IsZero() == false {
			if old, ok := s.lastTick[pid]; ok && p.CPUTicks >= old {
				p.CPU = 100 * float64(p.CPUTicks-old) / (s.hz * dt)
			}
		}
		procs = append(procs, p)
	}
	s.lastAt, s.lastTick = now, next
	sort.Slice(procs, func(i, j int) bool {
		if procs[i].PSSBytes != procs[j].PSSBytes {
			return procs[i].PSSBytes > procs[j].PSSBytes
		}
		return procs[i].PID < procs[j].PID
	})
	groupMap := map[string]*Group{}
	var snap Snapshot
	snap.At, snap.RootPID, snap.Processes, snap.Memory = now, rootPID, procs, MemoryStatus()
	for _, p := range procs {
		snap.TotalPSS += p.PSSBytes
		snap.TotalRSS += p.RSSBytes
		snap.TotalCPU += p.CPU
		g := groupMap[p.Kind]
		if g == nil {
			g = &Group{Kind: p.Kind}
			groupMap[p.Kind] = g
		}
		g.Count++
		g.PSSBytes += p.PSSBytes
		g.RSSBytes += p.RSSBytes
		g.CPU += p.CPU
	}
	for _, kind := range []string{"daemon", "tunnel", "browser", "lsp", "mcp", "shell", "other"} {
		if g := groupMap[kind]; g != nil {
			snap.Groups = append(snap.Groups, *g)
		}
	}
	return snap
}

func readProcess(pid int) (Process, bool) {
	stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return Process{}, false
	}
	text := string(stat)
	end := strings.LastIndex(text, ")")
	if end < 0 || end+2 >= len(text) {
		return Process{}, false
	}
	fields := strings.Fields(text[end+2:])
	if len(fields) < 22 {
		return Process{}, false
	}
	ppid, _ := strconv.Atoi(fields[1])
	ut, _ := strconv.ParseUint(fields[11], 10, 64)
	st, _ := strconv.ParseUint(fields[12], 10, 64)
	cmdBytes, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	command := strings.TrimSpace(strings.ReplaceAll(string(cmdBytes), "\x00", " "))
	if command == "" {
		command = strings.Trim(text[:end+1], "()")
	}
	kind, label := classify(pid, command)
	pss, rss := processMemory(pid)
	return Process{PID: pid, PPID: ppid, Command: command, Kind: kind, Label: label, PSSBytes: pss, RSSBytes: rss, CPUTicks: ut + st}, true
}

func processTree(root int) []int {
	if root <= 0 {
		return nil
	}
	entries, _ := os.ReadDir("/proc")
	children := map[int][]int{}
	for _, de := range entries {
		pid, err := strconv.Atoi(de.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", de.Name(), "stat"))
		if err != nil {
			continue
		}
		text := string(b)
		end := strings.LastIndex(text, ")")
		if end < 0 {
			continue
		}
		fields := strings.Fields(text[end+2:])
		if len(fields) < 2 {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err == nil {
			children[ppid] = append(children[ppid], pid)
		}
	}
	out := []int{}
	stack := []int{root}
	seen := map[int]bool{}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
		stack = append(stack, children[n]...)
	}
	return out
}

func processPSS(pid int) uint64 {
	pss, _ := processMemory(pid)
	return pss
}

func processMemory(pid int) (uint64, uint64) {
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "smaps_rollup"))
	if err == nil {
		var pss, rss uint64
		for _, line := range strings.Split(string(b), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			v, _ := strconv.ParseUint(fields[1], 10, 64)
			switch strings.TrimSuffix(fields[0], ":") {
			case "Pss":
				pss = v * 1024
			case "Rss":
				rss = v * 1024
			}
		}
		return pss, rss
	}
	b, err = os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "status"))
	if err != nil {
		return 0, 0
	}
	var rss uint64
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				v, _ := strconv.ParseUint(fields[1], 10, 64)
				rss = v * 1024
			}
		}
	}
	return rss, rss
}

func classify(pid int, command string) (string, string) {
	env := readEnviron(pid)
	if kind := env["COS_LITE_MANAGED"]; kind != "" {
		return kind, env["COS_LITE_MANAGED_LABEL"]
	}
	lower := strings.ToLower(command)
	switch {
	case strings.Contains(lower, "cos daemon"):
		return "daemon", "cos-lite"
	case strings.Contains(lower, "tunnel-client run"):
		return "tunnel", "openai"
	case strings.Contains(lower, "/chrome") || strings.Contains(lower, "chromium"):
		return "browser", "chromium"
	case strings.Contains(lower, "gopls") || strings.Contains(lower, "clangd") || strings.Contains(lower, "rust-analyzer") || strings.Contains(lower, "pyright") || strings.Contains(lower, "typescript-language-server"):
		return "lsp", filepath.Base(strings.Fields(command)[0])
	case strings.Contains(lower, "bash") || strings.Contains(lower, "zsh") || strings.Contains(lower, "sh -"):
		return "shell", filepath.Base(strings.Fields(command)[0])
	default:
		return "other", filepath.Base(strings.Fields(command)[0])
	}
}

func readEnviron(pid int) map[string]string {
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "environ"))
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, item := range strings.Split(string(b), "\x00") {
		if k, v, ok := strings.Cut(item, "="); ok {
			out[k] = v
		}
	}
	return out
}

func clockTicks() float64 {
	// Linux USER_HZ is 100 on the architectures supported by cos-lite.
	return 100
}

func Bytes(v uint64) string {
	const (
		KiB = 1 << 10
		MiB = 1 << 20
		GiB = 1 << 30
	)
	switch {
	case v >= GiB:
		return fmt.Sprintf("%.2f GiB", float64(v)/GiB)
	case v >= MiB:
		return fmt.Sprintf("%.1f MiB", float64(v)/MiB)
	case v >= KiB:
		return fmt.Sprintf("%.1f KiB", float64(v)/KiB)
	default:
		return fmt.Sprintf("%d B", v)
	}
}
