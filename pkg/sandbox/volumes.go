// Package sandbox: external storage volumes attached to a sandbox at boot.
//
// A volume is a plain sparse file on the host (never a dm-snapshot CoW — a data
// volume has no base image). It is handed to Cloud Hypervisor as an extra Raw
// disk in the initial vm.create and symlinked into the VMM's private tmpfs like
// the rootfs, so its disk path survives pause/resume via CH's snapshot config.
// The guest resolves the device by its virtio-blk serial and envd formats (if
// empty) and mounts it. Volumes are only ever attached at create time; they are
// freed (not deleted) when the capsule is destroyed.
package sandbox

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/wrenn/pkg/envdclient"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
	"git.omukk.dev/wrenn/wrenn/pkg/layout"
	"git.omukk.dev/wrenn/wrenn/pkg/vm"
)

// VolumeAttachSpec describes an external storage volume to attach to a sandbox
// at boot. Passed to Create by the host-agent RPC layer; the host provisions
// the backing file on first use.
type VolumeAttachSpec struct {
	VolumeID  pgtype.UUID
	TeamID    pgtype.UUID
	SizeMB    int
	MountPath string // "" => default /mnt/<vol-id>
}

// attachedVolume is the host's lean record of a volume attached to a sandbox.
// It doubles as the on-disk form (JSON tags) stored in runningState
// (cross-process re-attach) and snapshotMeta (pause/resume) — self-contained so
// a re-attaching or resuming process needs no re-derivation from IDs.
type attachedVolume struct {
	VolumeID  string `json:"volume_id"`  // formatted "vol-..." (logging + default mount path)
	HostPath  string `json:"host_path"`  // backing sparse file on the host
	Serial    string `json:"serial"`     // virtio-blk serial; guest resolves the device by this
	MountPath string `json:"mount_path"` // effective guest mount path
}

// volumeDisksFor returns the vm.VolumeDisk list for a set of attached volumes,
// used to rebuild the VM config on resume so CH's restored disk paths resolve.
func volumeDisksFor(vols []*attachedVolume) []vm.VolumeDisk {
	if len(vols) == 0 {
		return nil
	}
	disks := make([]vm.VolumeDisk, 0, len(vols))
	for _, v := range vols {
		disks = append(disks, vm.VolumeDisk{HostPath: v.HostPath, Serial: v.Serial})
	}
	return disks
}

// ensureVolumeFile makes sure the backing sparse file exists at path, creating
// it (and its parent dir) and truncating to sizeBytes on first use. Idempotent:
// an existing file is left untouched so previously-written data survives across
// detach and re-attach (size is fixed at first provision).
func ensureVolumeFile(path string, sizeBytes int64) error {
	if _, err := os.Stat(path); err == nil {
		return nil // already provisioned; preserve existing data
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat volume file: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create volume dir: %w", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create volume file: %w", err)
	}
	if err := f.Truncate(sizeBytes); err != nil {
		f.Close()
		os.Remove(path)
		return fmt.Errorf("truncate volume file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close volume file: %w", err)
	}
	return nil
}

// prepareVolumeDisks provisions the backing file for each spec and returns the
// vm.VolumeDisk list to bake into the VM config plus the attachedVolume records
// to track in sandbox state. Called before the VM boots.
func (m *Manager) prepareVolumeDisks(specs []VolumeAttachSpec) ([]vm.VolumeDisk, []*attachedVolume, error) {
	if len(specs) == 0 {
		return nil, nil, nil
	}
	disks := make([]vm.VolumeDisk, 0, len(specs))
	attached := make([]*attachedVolume, 0, len(specs))
	for _, spec := range specs {
		volIDStr := id.FormatVolumeID(spec.VolumeID)
		hostPath := layout.VolumeDataPath(m.cfg.WrennDir, spec.TeamID, spec.VolumeID)
		if err := ensureVolumeFile(hostPath, int64(spec.SizeMB)*1024*1024); err != nil {
			return nil, nil, fmt.Errorf("provision volume %s: %w", volIDStr, err)
		}
		serial := id.VolumeSerial(spec.VolumeID)
		mountPath := spec.MountPath
		if mountPath == "" {
			mountPath = id.DefaultVolumeMountPath(spec.VolumeID)
		}
		disks = append(disks, vm.VolumeDisk{HostPath: hostPath, Serial: serial})
		attached = append(attached, &attachedVolume{
			VolumeID:  volIDStr,
			HostPath:  hostPath,
			Serial:    serial,
			MountPath: mountPath,
		})
	}
	return disks, attached, nil
}

// mountVolumes asks envd to format (if empty) and mount every attached volume.
// Called once after a fresh boot. On resume the guest mount survives inside the
// restored memory image, so this is not called again.
func (m *Manager) mountVolumes(ctx context.Context, client *envdclient.Client, vols []*attachedVolume) error {
	for _, v := range vols {
		mctx, cancel := context.WithTimeout(ctx, m.cfg.EnvdTimeout)
		err := client.MountVolume(mctx, v.Serial, v.MountPath)
		cancel()
		if err != nil {
			return fmt.Errorf("mount volume %s: %w", v.VolumeID, err)
		}
	}
	return nil
}

// unmountVolumesBestEffort asks envd to sync+unmount each volume before a
// graceful destroy so buffered guest writes reach the backing file. Best-effort:
// on a crash envd is already gone and every call fails harmlessly.
func (m *Manager) unmountVolumesBestEffort(sb *sandboxState) {
	if len(sb.volumes) == 0 {
		return
	}
	client := sb.client.Load()
	if client == nil {
		return
	}
	for _, v := range sb.volumes {
		ctx, cancel := context.WithTimeout(context.Background(), m.cfg.EnvdTimeout)
		if err := client.UnmountVolume(ctx, v.MountPath); err != nil {
			slog.Warn("volume unmount on destroy failed", "id", sb.ID, "volume", v.VolumeID, "error", err)
		}
		cancel()
	}
}

// DeleteVolumeFile removes an external storage volume's backing file and its
// (now-empty) directory tree from this host. Called by the control plane when a
// detached volume is deleted. Idempotent: a missing file is not an error.
func (m *Manager) DeleteVolumeFile(teamID, volumeID pgtype.UUID) error {
	dir := layout.VolumeDir(m.cfg.WrennDir, teamID, volumeID)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove volume dir %s: %w", id.FormatVolumeID(volumeID), err)
	}
	return nil
}
