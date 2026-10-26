package pipeline

import "github.com/team-redbull/herzi/internal/config"

// Plan implements design Part 2, "Stage 4 — Plan (pure)":
// "big deficits go in waves, one request per pass".
func Plan(g Grow, p config.PoolConfig, f PoolFacts) []InstallRequest {
	n := min(g.Need, p.MaxOpenRunsPerPool-len(f.Open), APIMaxCount)
	if n <= 0 {
		return nil
	}
	return []InstallRequest{{Key: g.Key, Count: n}}
}
