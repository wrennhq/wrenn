// SPDX-License-Identifier: Apache-2.0

package cgroups

type NoopManager struct{}

var _ Manager = (*NoopManager)(nil)

func NewNoopManager() *NoopManager {
	return &NoopManager{}
}

func (n NoopManager) GetFileDescriptor(ProcessType) (int, bool) {
	return 0, false
}

func (n NoopManager) Close() error {
	return nil
}
