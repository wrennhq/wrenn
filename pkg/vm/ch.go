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
	return c.doJSON(ctx, method, path, body, nil)
}

// doJSON sends a request and optionally decodes a JSON response into out.
// out may be nil if the response body should be discarded.
func (c *chClient) doJSON(ctx context.Context, method, path string, body, out any) error {
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

	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("%s %s: decode response: %w", method, path, err)
		}
	}
	return nil
}

func boolPtr(b bool) *bool { return &b }

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
	Size   uint64 `json:"size"`
	Shared bool   `json:"shared,omitempty"`
	// Thp uses a pointer with NO omitempty so explicit false is always
	// serialized (CH defaults to true). Must be false so the backing memfile
	// remains 4 KiB-granular: balloon-reported free pages get punched as
	// holes and CH's SEEK_DATA/SEEK_HOLE snapshot writer (v52+) skips them.
	// A nil Thp would silently re-enable THP and break sparse snapshots —
	// rejecting "thp": null at the wire is preferable to a silent fallback.
	Thp           *bool  `json:"thp"`
	Prefault      bool   `json:"prefault,omitempty"`
	HotplugSize   uint64 `json:"hotplug_size,omitempty"`
	HotplugMethod string `json:"hotplug_method,omitempty"`
}

type chDisk struct {
	Path      string `json:"path"`
	Readonly  bool   `json:"readonly,omitempty"`
	ImageType string `json:"image_type,omitempty"`
	// Serial is exposed to the guest as the virtio-blk serial so envd can
	// resolve the device via /sys/block/*/serial. Only set for data volumes.
	Serial string `json:"serial,omitempty"`
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

	// Rootfs is always disk 0 (guest /dev/vda, referenced by root=/dev/vda in
	// the kernel cmdline). Data volumes follow as vdb, vdc, ... — but the guest
	// resolves each by serial, not by enumeration order.
	disks := []chDisk{
		{
			Path:      cfg.SandboxDir + "/rootfs.ext4",
			ImageType: "Raw",
		},
	}
	for _, v := range cfg.Volumes {
		disks = append(disks, chDisk{
			Path:      cfg.SandboxDir + "/" + v.symlinkName(),
			ImageType: "Raw",
			Serial:    v.Serial,
		})
	}

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
			Thp:    boolPtr(false),
		},
		Disks: disks,
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

// pauseVM freezes guest vCPUs and devices via the CH API.
func (c *chClient) pauseVM(ctx context.Context) error {
	return c.do(ctx, http.MethodPut, "/api/v1/vm.pause", nil)
}

// resumeVM unfreezes a paused VM via the CH API.
func (c *chClient) resumeVM(ctx context.Context) error {
	return c.do(ctx, http.MethodPut, "/api/v1/vm.resume", nil)
}

// snapshotVM dumps VM config + state + memory to a directory URL of the form
// `file:///abs/path/`. VM must be paused before calling.
func (c *chClient) snapshotVM(ctx context.Context, destURL string) error {
	return c.do(ctx, http.MethodPut, "/api/v1/vm.snapshot", map[string]string{
		"destination_url": destURL,
	})
}

// vmInfo reports the runtime state of the VM. Used after a restore to confirm
// CH successfully hydrated the snapshot before registering the VM.
func (c *chClient) vmInfo(ctx context.Context) (state string, err error) {
	var resp struct {
		State string `json:"state"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/vm.info", nil, &resp); err != nil {
		return "", err
	}
	return resp.State, nil
}
