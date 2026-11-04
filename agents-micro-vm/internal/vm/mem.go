package vm

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// MemStats is the Firecracker process's memory from /proc/<pid>/smaps_rollup,
// in KiB. RSS counts shared pages in full for every process that maps them,
// so it overstates the per-VM cost of forks. PSS divides each shared page by
// the number of processes mapping it, and Private is what you'd get back by
// killing this VM.
type MemStats struct {
	RSS, PSS, SharedClean, SharedDirty, PrivateClean, PrivateDirty, Swap int64
}

func (m MemStats) Private() int64 { return m.PrivateClean + m.PrivateDirty }
func (m MemStats) Shared() int64  { return m.SharedClean + m.SharedDirty }

func ReadMemStats(pid int) (MemStats, error) {
	f, err := os.Open(fmt.Sprintf("/proc/%d/smaps_rollup", pid))
	if err != nil {
		return MemStats{}, err
	}
	defer f.Close()
	var s MemStats
	fields := map[string]*int64{
		"Rss:": &s.RSS, "Pss:": &s.PSS,
		"Shared_Clean:": &s.SharedClean, "Shared_Dirty:": &s.SharedDirty,
		"Private_Clean:": &s.PrivateClean, "Private_Dirty:": &s.PrivateDirty,
		"Swap:": &s.Swap,
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		parts := strings.Fields(sc.Text())
		if len(parts) < 2 {
			continue
		}
		if p, ok := fields[parts[0]]; ok {
			*p, _ = strconv.ParseInt(parts[1], 10, 64)
		}
	}
	return s, sc.Err()
}

// MemAvailableKiB is the host's MemAvailable. The drop in it across starting
// N idle VMs, divided by N, is the honest "memory per VM" number: it includes
// page cache, page tables and KVM overhead that per-process counters miss.
func MemAvailableKiB() (int64, error) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "MemAvailable:") {
			f := strings.Fields(line)
			return strconv.ParseInt(f[1], 10, 64)
		}
	}
	return 0, fmt.Errorf("MemAvailable not found")
}

// DropCaches evicts the host page cache so the next restore reads the
// snapshot from disk. Needs root.
func DropCaches() error {
	return os.WriteFile("/proc/sys/vm/drop_caches", []byte("3\n"), 0)
}
