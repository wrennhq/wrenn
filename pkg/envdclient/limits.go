package envdclient

import (
	"fmt"
	"io"
)

// Guest-response size caps.
//
// A sandbox guest is root inside its own VM and controls every byte envd
// returns. Without a ceiling, a hostile guest can answer a host-agent request
// and then stream an endless body; the host buffers it whole (io.ReadAll /
// json.Decode / Connect envelope) and grows its heap until the process is
// OOM-killed — and one host agent serves every co-resident sandbox, so a
// single tenant could take the whole host (and every other tenant's VM) down.
// Every host-side read of a guest-controlled response must therefore be bounded
// by one of these limits.
const (
	// MaxEnvdControlBytes caps control/JSON responses (metrics, activity,
	// health, preload status, error bodies) and every per-message Connect RPC
	// (ListDir, ListProcesses, Exec frames, ...). connectrpc-go's default read
	// limit is 0, which means unlimited, so this must be applied explicitly via
	// connect.WithReadMaxBytes.
	MaxEnvdControlBytes = 16 << 20 // 16 MiB

	// maxEnvdFileBytes backstops the unary ReadFile path, which buffers a whole
	// guest file in memory. Genuinely large transfers should move to a streaming
	// API for constant-memory behaviour.
	maxEnvdFileBytes = 256 << 20 // 256 MiB

	// maxExecCaptureBytes caps total captured stdout+stderr for the
	// non-streaming Exec, which accumulates all output in memory. Beyond this the
	// call fails rather than growing without bound (e.g. `cat /dev/zero`).
	maxExecCaptureBytes = 64 << 20 // 64 MiB
)

// readCapped reads r fully but fails if the source would exceed max bytes,
// rather than silently truncating the way a bare io.LimitReader would. Use it
// where a truncated body would be mistaken for a valid (short) one.
func readCapped(r io.Reader, max int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("response exceeds %d-byte cap", max)
	}
	return data, nil
}
