package vm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// fcClient talks to the Firecracker HTTP API over a Unix socket.
type fcClient struct {
	http       *http.Client
	socketPath string
}

func newFCClient(socketPath string) *fcClient {
	return &fcClient{
		socketPath: socketPath,
		http: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", socketPath)
				},
			},
			Timeout: 10 * time.Second,
		},
	}
}

func (c *fcClient) do(ctx context.Context, method, path string, body any) error {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	// The host in the URL is ignored for Unix sockets; we use "localhost" by convention.
	req, err := http.NewRequestWithContext(ctx, method, "http://localhost"+path, bodyReader)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s %s: status %d: %s", method, path, resp.StatusCode, string(respBody))
	}

	return nil
}

// setBootSource configures the kernel and boot args.
func (c *fcClient) setBootSource(ctx context.Context, kernelPath, bootArgs string) error {
	return c.do(ctx, http.MethodPut, "/boot-source", map[string]string{
		"kernel_image_path": kernelPath,
		"boot_args":         bootArgs,
	})
}

// setRootfsDrive configures the root filesystem drive.
func (c *fcClient) setRootfsDrive(ctx context.Context, driveID, path string, readOnly bool) error {
	return c.do(ctx, http.MethodPut, "/drives/"+driveID, map[string]any{
		"drive_id":       driveID,
		"path_on_host":   path,
		"is_root_device": true,
		"is_read_only":   readOnly,
	})
}

// setNetworkInterface configures a network interface attached to a TAP device.
// A tx_rate_limiter caps sustained guest→host throughput to prevent user
// application traffic from completely saturating the TAP device and starving
// envd control traffic (PTY, exec, file ops).
func (c *fcClient) setNetworkInterface(ctx context.Context, ifaceID, tapName, macAddr string) error {
	return c.do(ctx, http.MethodPut, "/network-interfaces/"+ifaceID, map[string]any{
		"iface_id":      ifaceID,
		"host_dev_name": tapName,
		"guest_mac":     macAddr,
		"tx_rate_limiter": map[string]any{
			"bandwidth": map[string]any{
				"size":           209715200, // 200 MB/s sustained
				"refill_time":    1000,      // refill period: 1 second
				"one_time_burst": 104857600, // 100 MB initial burst
			},
		},
	})
}

// setMachineConfig configures vCPUs, memory, and other machine settings.
func (c *fcClient) setMachineConfig(ctx context.Context, vcpus, memMB int) error {
	return c.do(ctx, http.MethodPut, "/machine-config", map[string]any{
		"vcpu_count":   vcpus,
		"mem_size_mib": memMB,
		"smt":          false,
	})
}

// setMMDSConfig enables MMDS V2 token-based access on the given network interface.
// Must be called before startVM.
func (c *fcClient) setMMDSConfig(ctx context.Context, ifaceID string) error {
	return c.do(ctx, http.MethodPut, "/mmds/config", map[string]any{
		"version":            "V2",
		"network_interfaces": []string{ifaceID},
	})
}

// mmdsMetadata is the metadata payload written to the Firecracker MMDS store.
// envd reads this via PollForMMDSOpts to populate WRENN_SANDBOX_ID and WRENN_TEMPLATE_ID.
type mmdsMetadata struct {
	SandboxID  string `json:"instanceID"`
	TemplateID string `json:"envID"`
}

// setMMDS writes sandbox metadata to the Firecracker MMDS store.
// Can be called after the VM has started.
func (c *fcClient) setMMDS(ctx context.Context, sandboxID, templateID string) error {
	return c.do(ctx, http.MethodPut, "/mmds", mmdsMetadata{
		SandboxID:  sandboxID,
		TemplateID: templateID,
	})
}

// setBalloon configures the Firecracker balloon device for dynamic memory
// management. deflateOnOom lets the guest reclaim balloon pages under memory
// pressure. statsInterval enables periodic stats via GET /balloon/statistics.
// Must be called before startVM.
func (c *fcClient) setBalloon(ctx context.Context, amountMiB int, deflateOnOom bool, statsIntervalS int) error {
	return c.do(ctx, http.MethodPut, "/balloon", map[string]any{
		"amount_mib":             amountMiB,
		"deflate_on_oom":         deflateOnOom,
		"stats_polling_interval_s": statsIntervalS,
	})
}

// updateBalloon adjusts the balloon target at runtime.
func (c *fcClient) updateBalloon(ctx context.Context, amountMiB int) error {
	return c.do(ctx, http.MethodPatch, "/balloon", map[string]any{
		"amount_mib": amountMiB,
	})
}

// startVM issues the InstanceStart action.
func (c *fcClient) startVM(ctx context.Context) error {
	return c.do(ctx, http.MethodPut, "/actions", map[string]string{
		"action_type": "InstanceStart",
	})
}

// pauseVM pauses the microVM.
func (c *fcClient) pauseVM(ctx context.Context) error {
	return c.do(ctx, http.MethodPatch, "/vm", map[string]string{
		"state": "Paused",
	})
}

// resumeVM resumes a paused microVM.
func (c *fcClient) resumeVM(ctx context.Context) error {
	return c.do(ctx, http.MethodPatch, "/vm", map[string]string{
		"state": "Resumed",
	})
}

// createSnapshot creates a VM snapshot.
// snapshotType is "Full" (all memory) or "Diff" (only dirty pages since last resume).
func (c *fcClient) createSnapshot(ctx context.Context, snapPath, memPath, snapshotType string) error {
	return c.do(ctx, http.MethodPut, "/snapshot/create", map[string]any{
		"snapshot_type": snapshotType,
		"snapshot_path": snapPath,
		"mem_file_path": memPath,
	})
}

// loadSnapshotWithUffd loads a VM snapshot using a UFFD socket for
// lazy memory loading. Firecracker will connect to the socket and
// send the uffd fd + memory region mappings.
func (c *fcClient) loadSnapshotWithUffd(ctx context.Context, snapPath, uffdSocketPath string) error {
	return c.do(ctx, http.MethodPut, "/snapshot/load", map[string]any{
		"snapshot_path": snapPath,
		"resume_vm":     false,
		"mem_backend": map[string]any{
			"backend_type": "Uffd",
			"backend_path": uffdSocketPath,
		},
	})
}
