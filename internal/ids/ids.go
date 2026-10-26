// Package ids builds the Temporal workflow ids of install-server runs. They MUST equal the Python
// builders in the workflows repo (shared/workflow_ids.py, PR #12); ids_test.go pins both.
//
//	pool run : add-server-to-mce_<mce>_<infraenv>_<slot>
//	named run: add-server-to-mce_<mce>_<server>
//
// `_` joins every part because a Kubernetes name cannot contain it, so no two different
// (mce, infraenv, slot) triples build the same id.
package ids

import (
	"strconv"
	"strings"
)

const prefix = "add-server-to-mce"

// PoolSlot is the id of slot `slot` (1-based) of the pool `infraEnv` on hub `mce`.
func PoolSlot(mce, infraEnv string, slot int) string {
	return prefix + "_" + mce + "_" + infraEnv + "_" + strconv.Itoa(slot)
}

// Named is the id of an install of one named server on hub `mce` (server names are lowercased).
func Named(mce, server string) string {
	return prefix + "_" + mce + "_" + strings.ToLower(server)
}

// PoolPrefix is the prefix shared by every slot of one pool.
func PoolPrefix(mce, infraEnv string) string {
	return prefix + "_" + mce + "_" + infraEnv + "_"
}
