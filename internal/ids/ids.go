// Package ids builds the Temporal workflow ids of install-server runs. They MUST equal the Python
// builders in the workflows repo (shared/workflow_ids.py, PR #12); ids_test.go pins both.
//
//	pool run : install-server-<mce>_<infraenv>_<slot>
//	named run: install-server-<mce>_<server>
//
// `-` follows install-server; `_` separates the hub, pool and slot. Kubernetes names cannot
// contain `_`, so distinct (mce, infraenv, slot) triples build distinct ids.
package ids

import (
	"strconv"
	"strings"
)

const prefix = "install-server"

// PoolSlot is the id of slot `slot` (1-based) of the pool `infraEnv` on hub `mce`.
func PoolSlot(mce, infraEnv string, slot int) string {
	return prefix + "-" + mce + "_" + infraEnv + "_" + strconv.Itoa(slot)
}

// Named is the id of an install of one named server on hub `mce` (server names are lowercased).
func Named(mce, server string) string {
	return prefix + "-" + mce + "_" + strings.ToLower(server)
}

// PoolPrefix is the prefix shared by every slot of one pool.
func PoolPrefix(mce, infraEnv string) string {
	return prefix + "-" + mce + "_" + infraEnv + "_"
}
