package vm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
)

// chClient talks to the Cloud Hypervisor HTTP API over a Unix socket.
type chClient struct {
	http       *http.Client
	socketPath string
}

func newCHClient(socketPath string) *chClient {
	return &chClient{
		socketPath: socketPath,
		http: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", socketPath)
				},
			},
		},
	}
}

func (c *chClient) do(ctx context.Context, method, path string, body any) error {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

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

// --- CH API payload types ---

type chPayload struct {
	Firmware string `json:"firmware,omitempty"`
	Kernel   string `json:"kernel"`
	Cmdline  string `json:"cmdline"`
}

type chCPUs struct {
	BootVCPUs int `json:"boot_vcpus"`
	MaxVCPUs  int `json:"max_vcpus"`
}

type chMemory struct {
	Size          uint64 `json:"size"`
	Shared        bool   `json:"shared,omitempty"`
	HotplugSize   uint64 `json:"hotplug_size,omitempty"`
	HotplugMethod string `json:"hotplug_method,omitempty"`
}

type chDisk struct {
	Path      string `json:"path"`
	Readonly  bool   `json:"readonly,omitempty"`
	ImageType string `json:"image_type,omitempty"`
}

type chNet struct {
	Tap    string `json:"tap"`
	MAC    string `json:"mac"`
	NumQs  int    `json:"num_queues,omitempty"`
	QueueS int    `json:"queue_size,omitempty"`
}

type chBalloon struct {
	Size         int64 `json:"size"`
	DeflateOnOOM bool  `json:"deflate_on_oom"`
	FreePageRep  bool  `json:"free_page_reporting,omitempty"`
}

type chConsole struct {
	Mode string `json:"mode"`
}

type chCreatePayload struct {
	Payload chPayload  `json:"payload"`
	CPUs    chCPUs     `json:"cpus"`
	Memory  chMemory   `json:"memory"`
	Disks   []chDisk   `json:"disks"`
	Net     []chNet    `json:"net"`
	Balloon *chBalloon `json:"balloon,omitempty"`
	Serial  chConsole  `json:"serial"`
	Console chConsole  `json:"console"`
}

// createVM sends the full VM configuration as a single payload.
func (c *chClient) createVM(ctx context.Context, cfg *VMConfig) error {
	memBytes := uint64(cfg.MemoryMB) * 1024 * 1024

	payload := chCreatePayload{
		Payload: chPayload{
			Kernel:  cfg.KernelPath,
			Cmdline: cfg.kernelArgs(),
		},
		CPUs: chCPUs{
			BootVCPUs: cfg.VCPUs,
			MaxVCPUs:  cfg.VCPUs,
		},
		Memory: chMemory{
			Size:   memBytes,
			Shared: true,
		},
		Disks: []chDisk{
			{
				Path:      cfg.SandboxDir + "/rootfs.ext4",
				ImageType: "Raw",
			},
		},
		Net: []chNet{
			{
				Tap: cfg.TapDevice,
				MAC: cfg.TapMAC,
			},
		},
		Balloon: &chBalloon{
			Size:         0,
			DeflateOnOOM: true,
			FreePageRep:  true,
		},
		Serial: chConsole{
			Mode: "Tty",
		},
		Console: chConsole{
			Mode: "Off",
		},
	}

	return c.do(ctx, http.MethodPut, "/api/v1/vm.create", payload)
}

// bootVM starts the VM after creation.
func (c *chClient) bootVM(ctx context.Context) error {
	return c.do(ctx, http.MethodPut, "/api/v1/vm.boot", nil)
}

// pauseVM pauses the microVM.
func (c *chClient) pauseVM(ctx context.Context) error {
	return c.do(ctx, http.MethodPut, "/api/v1/vm.pause", nil)
}

// resumeVM resumes a paused microVM.
func (c *chClient) resumeVM(ctx context.Context) error {
	return c.do(ctx, http.MethodPut, "/api/v1/vm.resume", nil)
}

// snapshotVM creates a VM snapshot to the given directory.
func (c *chClient) snapshotVM(ctx context.Context, destURL string) error {
	return c.do(ctx, http.MethodPut, "/api/v1/vm.snapshot", map[string]string{
		"destination_url": destURL,
	})
}

// restoreVM restores a VM from a snapshot via the API. Uses OnDemand memory
// restore mode for UFFD-based lazy page loading — only pages the guest
// actually touches are faulted in from disk.
func (c *chClient) restoreVM(ctx context.Context, sourceURL string) error {
	return c.do(ctx, http.MethodPut, "/api/v1/vm.restore", map[string]any{
		"source_url":          sourceURL,
		"memory_restore_mode": "OnDemand",
		"resume":              true,
	})
}

// shutdownVMM cleanly shuts down the Cloud Hypervisor VMM process.
func (c *chClient) shutdownVMM(ctx context.Context) error {
	return c.do(ctx, http.MethodPut, "/api/v1/vmm.shutdown", nil)
}

// resizeBalloon adjusts the balloon target at runtime.
// sizeBytes is memory to take FROM the guest (0 = give all back).
func (c *chClient) resizeBalloon(ctx context.Context, sizeBytes int64) error {
	return c.do(ctx, http.MethodPut, "/api/v1/vm.resize", map[string]int64{
		"desired_balloon": sizeBytes,
	})
}
