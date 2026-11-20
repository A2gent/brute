package http

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
)

const (
	concurrencyMonitorInterval   = 10 * time.Second
	concurrencyCPUSampleWindow   = 250 * time.Millisecond
	concurrencyGPUCommandTimeout = 3 * time.Second
)

type resourceReading struct {
	CPUPercent   float64  `json:"cpu_percent"`
	RAMPercent   float64  `json:"ram_percent"`
	GPUPercent   *float64 `json:"gpu_percent,omitempty"`
	GPUAvailable bool     `json:"gpu_available"`
}

var ioregGPUUtilization = regexp.MustCompile(`"Device Utilization %"\s*=\s*(\d+)`)

func sampleResourceReading() resourceReading {
	var reading resourceReading
	if pct, err := cpu.Percent(concurrencyCPUSampleWindow, false); err == nil && len(pct) > 0 {
		reading.CPUPercent = pct[0]
	}
	if vm, err := mem.VirtualMemory(); err == nil {
		reading.RAMPercent = vm.UsedPercent
	}
	if gpu, ok := sampleGPUUtilization(); ok {
		reading.GPUPercent = &gpu
		reading.GPUAvailable = true
	}
	return reading
}

func sampleGPUUtilization() (float64, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), concurrencyGPUCommandTimeout)
	defer cancel()
	if runtime.GOOS == "darwin" {
		out, err := exec.CommandContext(ctx, "ioreg", "-r", "-d", "1", "-c", "IOAccelerator").Output()
		if err != nil {
			return 0, false
		}
		return parseIoregGPUUtilization(string(out))
	}
	if _, err := exec.LookPath("nvidia-smi"); err != nil {
		return 0, false
	}
	gpu := queryNvidiaGPU(resourceGPU{})
	return gpu.UtilizationPercent, gpu.Available
}

// parseIoregGPUUtilization returns the busiest accelerator, since any saturated GPU blocks new work.
func parseIoregGPUUtilization(out string) (float64, bool) {
	found := false
	busiest := 0.0
	for _, match := range ioregGPUUtilization.FindAllStringSubmatch(out, -1) {
		value, err := strconv.ParseFloat(match[1], 64)
		if err != nil {
			continue
		}
		found = true
		busiest = max(busiest, value)
	}
	return busiest, found
}

// currentResourceReading returns the last background sample; admission never samples itself.
func (s *Server) currentResourceReading() resourceReading {
	st := s.concurrencyState()
	st.sampleMu.Lock()
	defer st.sampleMu.Unlock()
	if st.sample == nil {
		return resourceReading{}
	}
	return *st.sample
}

func (s *Server) refreshResourceReading() {
	st := s.concurrencyState()
	st.sampleMu.Lock()
	sampler := st.sampler
	st.sampleMu.Unlock()
	reading := sampler()
	st.sampleMu.Lock()
	st.sample = &reading
	st.sampleMu.Unlock()
}

func resourceBlockReasonFor(cfg concurrencyConfig, reading resourceReading) string {
	if cfg.MaxCPUPercent > 0 && reading.CPUPercent > float64(cfg.MaxCPUPercent) {
		return fmt.Sprintf("CPU load %.0f%% is above %d%%", reading.CPUPercent, cfg.MaxCPUPercent)
	}
	if cfg.MaxRAMPercent > 0 && reading.RAMPercent > float64(cfg.MaxRAMPercent) {
		return fmt.Sprintf("RAM usage %.0f%% is above %d%%", reading.RAMPercent, cfg.MaxRAMPercent)
	}
	if cfg.MaxGPUPercent > 0 && reading.GPUPercent != nil && *reading.GPUPercent > float64(cfg.MaxGPUPercent) {
		return fmt.Sprintf("GPU load %.0f%% is above %d%%", *reading.GPUPercent, cfg.MaxGPUPercent)
	}
	return ""
}

func (s *Server) resourceBlockReason(cfg concurrencyConfig) string {
	if !cfg.resourceLimitsEnabled() {
		return ""
	}
	return resourceBlockReasonFor(cfg, s.currentResourceReading())
}

// runConcurrencyMonitor re-checks load so sessions held back by thresholds start once it drops,
// and doubles as a safety net that re-triggers queues whenever limits are configured.
func (s *Server) runConcurrencyMonitor(ctx context.Context) {
	ticker := time.NewTicker(concurrencyMonitorInterval)
	defer ticker.Stop()
	s.refreshResourceReading()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		s.refreshResourceReading()
		cfg := s.loadConcurrencyConfig()
		if !cfg.anyLimitEnabled() || s.resourceBlockReason(cfg) != "" {
			continue
		}
		s.scheduleSessionQueueResume()
	}
}
