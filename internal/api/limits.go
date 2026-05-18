package api

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/wrenn/pkg/cpextension"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
)

// collectLimitsProvider returns the first extension that implements
// LimitsProvider. Exactly-one semantics — a deployment with multiple billing
// providers is misconfigured and we surface that loudly via the first match.
func collectLimitsProvider(extensions []cpextension.Extension) cpextension.LimitsProvider {
	for _, ext := range extensions {
		if p, ok := ext.(cpextension.LimitsProvider); ok {
			return p
		}
	}
	return nil
}

// collectUsageProvider returns the first extension UsageProvider, or a
// DB-backed default that reads sandbox_metrics views. The default is used
// whenever a LimitsProvider is configured but the cloud repo hasn't shipped
// its own usage source yet.
func collectUsageProvider(extensions []cpextension.Extension, queries *db.Queries) cpextension.UsageProvider {
	for _, ext := range extensions {
		if p, ok := ext.(cpextension.UsageProvider); ok {
			return p
		}
	}
	return &defaultUsageProvider{queries: queries}
}

type defaultUsageProvider struct {
	queries *db.Queries
}

func (p *defaultUsageProvider) CurrentUsage(ctx context.Context, teamID pgtype.UUID) (cpextension.Usage, error) {
	row, err := p.queries.GetLiveMetrics(ctx, teamID)
	if err != nil {
		return cpextension.Usage{}, fmt.Errorf("get live metrics: %w", err)
	}
	return cpextension.Usage{
		ConcurrentSandboxes: int(row.RunningCount),
		VCPUsInUse:          int(row.VcpusReserved),
		MemoryMBInUse:       int(row.MemoryMbReserved),
	}, nil
}

// quotaCheckResult bundles the limits + usage retrieval result and the
// outcome of comparing them against a prospective new sandbox.
type quotaCheckResult struct {
	limited bool
	code    string
	message string
}

// enforceLimits applies the LimitsProvider gate to a prospective new sandbox
// of the given size. Returns (ok, code, message) — when ok is false the
// caller must respond 402 Payment Required with the supplied code/message.
//
// If no LimitsProvider extension is installed, the OSS deployment is treated
// as unmetered and every request passes.
func enforceLimits(
	ctx context.Context,
	lp cpextension.LimitsProvider,
	up cpextension.UsageProvider,
	teamID pgtype.UUID,
	vcpus, memoryMB int32,
) quotaCheckResult {
	if lp == nil {
		return quotaCheckResult{}
	}
	limits, err := lp.EffectiveLimits(ctx, teamID)
	if err != nil {
		return quotaCheckResult{limited: true, code: "limits_unavailable", message: "unable to determine plan limits"}
	}
	usage, err := up.CurrentUsage(ctx, teamID)
	if err != nil {
		return quotaCheckResult{limited: true, code: "usage_unavailable", message: "unable to determine current usage"}
	}
	if limits.MaxConcurrentSandboxes > 0 && usage.ConcurrentSandboxes+1 > limits.MaxConcurrentSandboxes {
		return quotaCheckResult{limited: true, code: "concurrent_sandbox_limit", message: fmt.Sprintf("plan allows %d concurrent sandboxes", limits.MaxConcurrentSandboxes)}
	}
	if limits.MaxVCPUs > 0 && usage.VCPUsInUse+int(vcpus) > limits.MaxVCPUs {
		return quotaCheckResult{limited: true, code: "vcpu_limit", message: fmt.Sprintf("plan allows %d vCPUs in use", limits.MaxVCPUs)}
	}
	if limits.MaxMemoryMB > 0 && usage.MemoryMBInUse+int(memoryMB) > limits.MaxMemoryMB {
		return quotaCheckResult{limited: true, code: "memory_limit", message: fmt.Sprintf("plan allows %d MB memory in use", limits.MaxMemoryMB)}
	}
	return quotaCheckResult{}
}
