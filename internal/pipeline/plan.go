package pipeline

import "github.com/team-redbull/herzi/internal/config"

func Plan(g Grow, p config.PoolConfig, f PoolFacts) []InstallRequest {
	n := min(g.Need, p.MaxOpenRunsPerPool-len(f.Open), APIMaxCount)
	if n <= 0 {
		return nil
	}
	return []InstallRequest{{Key: g.Key, Count: n}}
}
