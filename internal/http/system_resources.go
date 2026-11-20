package http

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
)

const resourceSampleInterval = time.Second

type resourceCPU struct {
	UsagePercent float64 `json:"usage_percent"`
	Cores        int     `json:"cores"`
}

type resourceMemory struct {
	TotalBytes     uint64  `json:"total_bytes"`
	UsedBytes      uint64  `json:"used_bytes"`
	AvailableBytes uint64  `json:"available_bytes"`
	UsedPercent    float64 `json:"used_percent"`
}

type resourceGPU struct {
	Available          bool    `json:"available"`
	Name               string  `json:"name"`
	Source             string  `json:"source"`
	MemoryTotalBytes   uint64  `json:"memory_total_bytes"`
	MemoryUsedBytes    uint64  `json:"memory_used_bytes"`
	UtilizationPercent float64 `json:"utilization_percent"`
}

type resourceNetwork struct {
	RxBytesPerSec float64 `json:"rx_bytes_per_sec"`
	TxBytesPerSec float64 `json:"tx_bytes_per_sec"`
}

type resourceDiskIO struct {
	ReadBytesPerSec  float64 `json:"read_bytes_per_sec"`
	WriteBytesPerSec float64 `json:"write_bytes_per_sec"`
}

type resourceDisk struct {
	Path        string  `json:"path"`
	TotalBytes  uint64  `json:"total_bytes"`
	FreeBytes   uint64  `json:"free_bytes"`
	UsedPercent float64 `json:"used_percent"`
}

type systemResources struct {
	CapturedAt string          `json:"captured_at"`
	CPU        resourceCPU     `json:"cpu"`
	Memory     resourceMemory  `json:"memory"`
	GPU        resourceGPU     `json:"gpu"`
	Network    resourceNetwork `json:"network"`
	DiskIO     resourceDiskIO  `json:"disk_io"`
	Disk       resourceDisk    `json:"disk"`
}

type counterSample struct {
	at                  time.Time
	netRx, netTx        uint64
	diskRead, diskWrite uint64
}

func counterRate(prev, cur uint64, elapsed time.Duration) float64 {
	if elapsed <= 0 || cur < prev {
		return 0
	}
	return float64(cur-prev) / elapsed.Seconds()
}

type resourceSampler struct {
	mu       sync.Mutex
	last     *counterSample
	snapshot *systemResources
	diskPath string
	once     sync.Once
	gpuOnce  sync.Once
	gpuInfo  resourceGPU
}

var globalResourceSampler = &resourceSampler{}

func resourceDiskPath(dataPath string) string {
	if strings.TrimSpace(dataPath) != "" {
		if _, err := os.Stat(dataPath); err == nil {
			return dataPath
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "/"
	}
	return home
}

func (rs *resourceSampler) start(diskPath string) {
	rs.once.Do(func() {
		rs.mu.Lock()
		rs.diskPath = diskPath
		rs.mu.Unlock()
		go func() {
			ticker := time.NewTicker(resourceSampleInterval)
			defer ticker.Stop()
			for range ticker.C {
				rs.sample()
			}
		}()
	})
}

func (rs *resourceSampler) latest(diskPath string) systemResources {
	rs.start(diskPath)
	rs.mu.Lock()
	snap := rs.snapshot
	rs.mu.Unlock()
	if snap == nil {
		return rs.sample()
	}
	return *snap
}

func (rs *resourceSampler) sample() systemResources {
	now := time.Now()
	out := systemResources{CapturedAt: now.UTC().Format(time.RFC3339), CPU: resourceCPU{Cores: runtime.NumCPU()}}

	if pct, err := cpu.Percent(0, false); err == nil && len(pct) > 0 {
		out.CPU.UsagePercent = pct[0]
	}
	if vm, err := mem.VirtualMemory(); err == nil {
		out.Memory = resourceMemory{TotalBytes: vm.Total, UsedBytes: vm.Used, AvailableBytes: vm.Available, UsedPercent: vm.UsedPercent}
	}

	cur := counterSample{at: now}
	if nio, err := net.IOCounters(false); err == nil && len(nio) > 0 {
		cur.netRx, cur.netTx = nio[0].BytesRecv, nio[0].BytesSent
	}
	if dio, err := disk.IOCounters(); err == nil {
		for _, c := range dio {
			cur.diskRead += c.ReadBytes
			cur.diskWrite += c.WriteBytes
		}
	}

	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.last != nil {
		elapsed := now.Sub(rs.last.at)
		out.Network = resourceNetwork{RxBytesPerSec: counterRate(rs.last.netRx, cur.netRx, elapsed), TxBytesPerSec: counterRate(rs.last.netTx, cur.netTx, elapsed)}
		out.DiskIO = resourceDiskIO{ReadBytesPerSec: counterRate(rs.last.diskRead, cur.diskRead, elapsed), WriteBytesPerSec: counterRate(rs.last.diskWrite, cur.diskWrite, elapsed)}
	}
	rs.last = &cur

	path := rs.diskPath
	if path == "" {
		path = resourceDiskPath("")
	}
	out.Disk.Path = path
	if du, err := disk.Usage(path); err == nil {
		out.Disk.TotalBytes, out.Disk.FreeBytes, out.Disk.UsedPercent = du.Total, du.Free, du.UsedPercent
	}

	rs.gpuOnce.Do(func() { rs.gpuInfo = detectGPU(out.Memory.TotalBytes) })
	out.GPU = rs.gpuInfo
	if out.GPU.Source == "nvidia-smi" {
		out.GPU = queryNvidiaGPU(out.GPU)
	}
	if out.GPU.Source == "apple_unified" {
		out.GPU.MemoryUsedBytes = out.Memory.UsedBytes
	}

	rs.snapshot = &out
	return out
}

func detectGPU(totalMemory uint64) resourceGPU {
	if path, err := exec.LookPath("nvidia-smi"); err == nil && path != "" {
		gpu := queryNvidiaGPU(resourceGPU{Source: "nvidia-smi"})
		if gpu.Available {
			return gpu
		}
	}
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		name := "Apple Silicon GPU"
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, "sysctl", "-n", "machdep.cpu.brand_string").Output(); err == nil && strings.TrimSpace(string(out)) != "" {
			name = strings.TrimSpace(string(out))
		}
		return resourceGPU{Available: true, Name: name, Source: "apple_unified", MemoryTotalBytes: totalMemory}
	}
	return resourceGPU{}
}

func queryNvidiaGPU(base resourceGPU) resourceGPU {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "nvidia-smi", "--query-gpu=name,memory.total,memory.used,utilization.gpu", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return base
	}
	line := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	parts := strings.Split(line, ",")
	if len(parts) < 4 {
		return base
	}
	totalMiB, _ := strconv.ParseUint(strings.TrimSpace(parts[1]), 10, 64)
	usedMiB, _ := strconv.ParseUint(strings.TrimSpace(parts[2]), 10, 64)
	util, _ := strconv.ParseFloat(strings.TrimSpace(parts[3]), 64)
	return resourceGPU{
		Available:          true,
		Name:               strings.TrimSpace(parts[0]),
		Source:             "nvidia-smi",
		MemoryTotalBytes:   totalMiB << 20,
		MemoryUsedBytes:    usedMiB << 20,
		UtilizationPercent: util,
	}
}

func (s *Server) handleSystemResources(w http.ResponseWriter, r *http.Request) {
	dataPath := ""
	if s.config != nil {
		dataPath = filepath.Clean(s.config.DataPath)
	}
	s.jsonResponse(w, http.StatusOK, globalResourceSampler.latest(resourceDiskPath(dataPath)))
}
