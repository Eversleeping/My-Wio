package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	gnet "github.com/shirou/gopsutil/v4/net"

	"github.com/wio-platform/wio/internal/capabilities"
	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/childprocess"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/scanner"
)

func (c *Client) refreshInventory(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	if err := c.enqueueInventory(ctx); err != nil {
		c.log.Warn("could not refresh inventory after project creation", "error", err)
	}
}

func (c *Client) enqueueHeartbeat(ctx context.Context) error {
	hostname, _ := os.Hostname()
	codexPath := c.currentCodexPath()
	heartbeat := protocol.Heartbeat{Hostname: hostname, AgentVersion: buildinfo.Version, CodexVersion: commandVersion(ctx, codexPath), CodexReady: commandAvailable(codexPath), ScanRoots: c.inventoryRoots(), ManagedRoots: c.managedRoots()}
	geminiPath := c.gemini.Command()
	heartbeat.GeminiVersion = c.currentGeminiVersion(ctx, geminiPath)
	heartbeat.GeminiReady = commandAvailable(geminiPath)
	claudePath := effectiveClaudePath(c.config.StateDir)
	heartbeat.ClaudeVersion = c.currentClaudeVersion(ctx, claudePath)
	heartbeat.ClaudeReady = commandAvailable(claudePath)
	heartbeat.Capabilities = capabilities.AgentDeclaration(heartbeat.GeminiVersion, heartbeat.ClaudeVersion)
	return c.queue(protocol.AgentKindHeartbeat, heartbeat, false)
}

func (c *Client) enqueueMetrics(ctx context.Context) error {
	percentages, _ := cpu.PercentWithContext(ctx, 250*time.Millisecond, false)
	memory, _ := mem.VirtualMemoryWithContext(ctx)
	usage, _ := disk.UsageWithContext(ctx, "/")
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
