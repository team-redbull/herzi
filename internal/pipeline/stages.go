package pipeline

import (
	"time"

	"github.com/team-redbull/herzi/internal/config"
)

// The four stages. Signatures are the contract between the pipeline and the controller;
// the bodies are implemented in attribute.go, validate.go, decide.go and plan.go.
//
//	func Attribute(raw RawSnapshot, s Settings) map[PoolKey]PoolFacts
//	func Validate(f PoolFacts, raw RawSnapshot, p config.PoolConfig) Verdict
//	func Decide(f PoolFacts, p config.PoolConfig, now time.Time) Intent
//	func Plan(g Grow, p config.PoolConfig, f PoolFacts) []InstallRequest
var (
	_ = time.Time{}
	_ config.PoolConfig
)
