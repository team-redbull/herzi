// Package ids builds install-server workflow IDs compatible with the Python workflows service.
package ids

import (
	"strconv"
	"strings"
)

const prefix = "install-server"

// PoolSlot uses a one-based slot number.
func PoolSlot(mce, infraEnv string, slot int) string {
	return prefix + "-" + mce + "_" + infraEnv + "_" + strconv.Itoa(slot)
}

// Named lowercases server names to match the workflows service.
func Named(mce, server string) string {
	return prefix + "-" + mce + "_" + strings.ToLower(server)
}

func PoolPrefix(mce, infraEnv string) string {
	return prefix + "-" + mce + "_" + infraEnv + "_"
}
