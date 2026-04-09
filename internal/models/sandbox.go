package models

import (
	"net"
	"time"
)

// SandboxStatus represents the current state of a sandbox.
type SandboxStatus string

const (
	StatusPending SandboxStatus = "pending"
	StatusRunning SandboxStatus = "running"
	StatusPaused  SandboxStatus = "paused"
	StatusStopped SandboxStatus = "stopped"
	StatusError   SandboxStatus = "error"
)

// Sandbox holds all state for a running sandbox on this host.
type Sandbox struct {
	ID             string
	Status         SandboxStatus
	TemplateTeamID [16]byte
	TemplateID     [16]byte
	VCPUs          int
	MemoryMB       int
	TimeoutSec     int
	SlotIndex      int
	HostIP         net.IP
	RootfsPath     string
	CreatedAt      time.Time
	LastActiveAt   time.Time
}
