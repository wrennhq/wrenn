// SPDX-License-Identifier: Apache-2.0

package keys

type Hasher interface {
	Hash(key []byte) string
}
