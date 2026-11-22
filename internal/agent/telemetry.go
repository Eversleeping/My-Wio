package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	gnet "github.com/shirou/gopsutil/v4/net"

	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/capabilities"
	"github.com/wio-platform/wio/internal/childprocess"
	"github.com/wio-platform/wio/internal/codexcli"
	"github.com/wio-platform/wio/internal/hostplatform"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/scanner"
)

type maintenanceTask struct {
	interval time.Duration
	timeout  time.Duration
	collect  func(context.Context) error
}

// Each task runs serially in its own worker. Non-cancellable platform calls can
// delay that collector, while heartbeats and control traffic remain independent.
func (c *Client) startMaintenance(ctx context.Context, tasks ...maintenanceTask) func() {
	wakes := make([]chan struct{}, len(tasks))
	for index, task := range tasks {
		wake := make(chan struct{}, 1)
		wakes[index] = wake
		c.startWorker(func() {
			ticker := time.NewTicker(task.interval)
			defer ticker.Stop()
			for ctx.Err() == nil {
				collectCtx, cancel := context.WithTimeout(ctx, task.timeout)
				_ = task.collect(collectCtx)
				cancel()
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				case <-wake:
				}
			}
		})
	}
	return func() {
		for _, wake := range wakes {
			select {
			case wake <- struct{}{}:
			default:
			}
		}
	}
}

func (c *Client) connectedCollector(collect func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) error {
		if !c.controlConnected.Load() {
			return nil
		}
		return collect(ctx)
	}
}

func (c *Client) refreshInventory(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	if err := c.enqueueInventory(ctx); err != nil {
		c.log.Warn("could not refresh inventory after project creation", "error", err)
	}
}

func (c *Client) maintainOperationReceipts(context.Context) error {
	c.pruneOperationReceipts(time.Now())
	c.seenMu.Lock()
	c.pruneCompletedOperations(time.Now())
	c.seenMu.Unlock()
	return nil
}

type cliState struct {
	version string
	ready   bool
}

type cliInventory struct {
	codex, gemini, claude cliState
}

func (c *Client) seedCLIInventory() {
	for _, cli := range []struct{ kind, path string }{{"codex", c.currentCodexPath()}, {"gemini", c.gemini.Command()}, {"claude", effectiveClaudePath(c.config.StateDir)}} {
		state := cliState{ready: commandAvailable(cli.path)}
		raw, _ := os.ReadFile(filepath.Join(c.config.StateDir, cli.kind, codexCurrentVersionFile))
		version := strings.TrimSpace(string(raw))
		if state.ready && codexcli.ValidTargetVersion(version) && cli.path == managedCLIExecutable(filepath.Join(c.config.StateDir, cli.kind, "versions", version), cli.kind) {
			state.version = version
			if cli.kind == "codex" {
				state.version = "codex-cli " + version
			} else if cli.kind == "claude" {
				state.version += " (Claude Code)"
			}
		}
		c.updateCLIInventory(context.Background(), cli.kind, state)
	}
}

func (c *Client) updateCLIInventory(ctx context.Context, kind string, state cliState) {
	c.cliInventoryMu.Lock()
	var previous cliState
	switch kind {
	case "codex":
		previous, c.cliInventory.codex = c.cliInventory.codex, state
	case "gemini":
		previous, c.cliInventory.gemini = c.cliInventory.gemini, state
	case "claude":
		previous, c.cliInventory.claude = c.cliInventory.claude, state
	}
	c.cliInventoryMu.Unlock()
	if previous != state && c.controlConnected.Load() {
		_ = c.enqueueHeartbeat(ctx)
	}
}

func (c *Client) refreshCLIInventory(ctx context.Context) error {
	ctx = c.processContext(ctx)
	for _, cli := range []struct {
		kind    string
		path    func() string
		version func(context.Context, string) string
	}{
		{"codex", c.currentCodexPath, readCodexUpdateVersion},
		{"gemini", c.gemini.Command, c.currentGeminiVersion},
		{"claude", func() string { return effectiveClaudePath(c.config.StateDir) }, c.currentClaudeVersion},
	} {
		if err := ctx.Err(); err != nil {
			return err
		}
		path := cli.path()
		version := cli.version(ctx, path)
		if err := ctx.Err(); err != nil {
			return err
		}
		c.updateCLIInventory(ctx, cli.kind, cliState{version, commandAvailable(path)})
	}
	return nil
}

func (c *Client) enqueueHeartbeat(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	hostname, _ := os.Hostname()
	c.cliInventoryMu.RLock()
	clis := c.cliInventory
	c.cliInventoryMu.RUnlock()
	heartbeat := protocol.Heartbeat{OS: runtime.GOOS, Architecture: runtime.GOARCH, Hostname: hostname, AgentVersion: buildinfo.Version, CodexVersion: clis.codex.version, CodexReady: clis.codex.ready, GeminiVersion: clis.gemini.version, GeminiReady: clis.gemini.ready, ClaudeVersion: clis.claude.version, ClaudeReady: clis.claude.ready, ScanRoots: c.inventoryRoots(), ManagedRoots: c.managedRoots()}
	heartbeat.Capabilities = capabilities.AgentDeclaration(heartbeat.GeminiVersion, heartbeat.ClaudeVersion)
	heartbeat.Capabilities = slices.DeleteFunc(heartbeat.Capabilities, func(name string) bool {
		if runtime.GOOS != "linux" && (name == protocol.CapabilityAgentAccess || name == protocol.CapabilityDeployRollbackExact) {
			return true
		}
		return c.config.configPath != hostplatform.Current().Config && (name == protocol.CapabilityAgentAccess || name == protocol.CapabilityAgentRestart)
	})
	return c.queue(protocol.AgentKindHeartbeat, heartbeat, false)
}

func (c *Client) enqueueMetrics(ctx context.Context) error {
	percentages, _ := cpu.PercentWithContext(ctx, 250*time.Millisecond, false)
	memory, _ := mem.VirtualMemoryWithContext(ctx)
	usage, _ := disk.UsageWithContext(ctx, metricDiskPath(c.config.StateDir))
	average, _ := load.AvgWithContext(ctx)
	interfaces, _ := gnet.IOCountersWithContext(ctx, false)
	metric := protocol.Metrics{}
	if len(percentages) > 0 {
		metric.CPUPercent = percentages[0]
	}
	if memory != nil {
		metric.MemoryPercent = memory.UsedPercent
		metric.MemoryUsedBytes = memory.Used
		metric.MemoryTotalBytes = memory.Total
	}
	if usage != nil {
		metric.DiskPercent = usage.UsedPercent
		metric.DiskUsedBytes = usage.Used
		metric.DiskTotalBytes = usage.Total
	}
	if average != nil {
		metric.Load1 = average.Load1
	}
	if len(interfaces) > 0 {
		metric.NetRxBytes = interfaces[0].BytesRecv
		metric.NetTxBytes = interfaces[0].BytesSent
	}
	return c.queue(protocol.AgentKindMetrics, metric, false)
}

func (c *Client) enqueueInventory(ctx context.Context) error {
	inventory, err := scanner.Discover(ctx, c.inventoryRoots(), 200)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.queue(protocol.AgentKindInventory, inventory, false)
}

func (c *Client) inventoryRoots() []string {
	roots := make([]string, 0, len(c.config.ScanRoots)+1)
	seen := make(map[string]bool, len(c.config.ScanRoots)+1)
	for _, root := range c.config.ScanRoots {
		root = filepath.Clean(strings.TrimSpace(root))
		if root == "." || seen[root] {
			continue
		}
		seen[root] = true
		roots = append(roots, root)
	}
	cloneRoot := filepath.Clean(strings.TrimSpace(c.config.CloneRoot))
	if cloneRoot == "." {
		return roots
	}
	for _, root := range roots {
		relative, err := filepath.Rel(root, cloneRoot)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return roots
		}
	}
	return append(roots, cloneRoot)
}

func (c *Client) managedRoots() []string {
	return c.inventoryRoots()
}

func commandVersion(ctx context.Context, command string) string {
	return commandVersionWithTimeout(ctx, command, 3*time.Second)
}

func commandVersionWithTimeout(ctx context.Context, command string, timeout time.Duration) string {
	versionContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output, err := childprocess.Output(childprocess.CommandContext(versionContext, command, "--version"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func commandAvailable(command string) bool {
	_, err := exec.LookPath(command)
	return err == nil
}

func metricDiskPath(state string) string {
	if runtime.GOOS == "windows" {
		return filepath.VolumeName(state) + `\`
	}
	return "/"
}
