// Package ids builds workflow IDs compatible with the Python workflows service.
package ids

import (
	"strconv"
	"strings"
)

// Workflow is the workflows service name of the install workflow: the id prefix and, with "-workflow", the queue.
const Workflow = "add-server-to-mce"

// PoolSlot uses a one-based slot number.
func PoolSlot(mce, infraEnv string, slot int) string {
	return Workflow + "-" + mce + "_" + infraEnv + "_" + strconv.Itoa(slot)
}

// Named lowercases server names to match the workflows service.
func Named(mce, server string) string {
	return Workflow + "-" + mce + "_" + strings.ToLower(server)
}

func PoolPrefix(mce, infraEnv string) string {
	return Workflow + "-" + mce + "_" + infraEnv + "_"
}

// RemoveWorkflow is the workflows service name of the remove workflow: the id prefix and, with "-workflow", the queue.
const RemoveWorkflow = "remove-server-from-mce"

// RemoveServer has no MCE in it; other workflows start the same id for their own removals.
func RemoveServer(server string) string { return RemoveWorkflow + "-" + strings.ToLower(server) }
