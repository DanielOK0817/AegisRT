package benchmark

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
)

const schedulerAnalysisSchemaV1 = "aegisrt.experiment.scheduler-analysis/v1"

type SchedulerAnalysis struct {
	SchemaVersion      string                  `json:"schema_version"`
	PlanID             string                  `json:"plan_id"`
	BootstrapSeed      int64                   `json:"bootstrap_seed"`
	BootstrapResamples int                     `json:"bootstrap_resamples"`
	Cells              []SchedulerAnalysisCell `json:"cells"`
}

type SchedulerAnalysisCell struct {
	CaseID         string                    `json:"case_id"`
	BaselinePolicy string                    `json:"baseline_policy"`
	Treatment      string                    `json:"treatment"`
	Metrics        []SchedulerMetricAnalysis `json:"metrics"`
}

type SchedulerMetricAnalysis struct {
	Name               string                `json:"name"`
	Direction          string                `json:"direction"`
	Baseline           SchedulerDistribution `json:"baseline"`
	Treatment          SchedulerDistribution `json:"treatment"`
	PairedN            int                   `json:"paired_n"`
	ImprovementMedian  float64               `json:"improvement_median"`
	ImprovementCILower float64               `json:"improvement_ci95_lower"`
	ImprovementCIUpper float64               `json:"improvement_ci95_upper"`
	RankBiserial       float64               `json:"paired_rank_biserial"`
}

type SchedulerDistribution struct {
	N      int     `json:"n"`
	Median float64 `json:"median"`
	P50    float64 `json:"p50"`
	P95    float64 `json:"p95"`
	IQR    float64 `json:"iqr"`
}

type schedulerMetricSpec struct {
	name       string
	direction  string
	value      func(SchedulerRow) float64
	higherBest bool
}

func analyzeSchedulerRows(planID string, rows []SchedulerRow) (SchedulerAnalysis, error) {
	groups := make(map[string][]SchedulerRow)
	for _, row := range rows {
		groups[row.PairID] = append(groups[row.PairID], row)
	}
	cellPairs := make(map[string][][2]SchedulerRow)
	for pairID, pair := range groups {
		if err := validatePair(pairID, pair); err != nil {
			return SchedulerAnalysis{}, err
		}
		baseline, treatment := pair[0], pair[1]
		if baseline.Role != "baseline" {
			baseline, treatment = treatment, baseline
		}
		key := baseline.CaseID + "/" + treatment.Policy
		cellPairs[key] = append(cellPairs[key], [2]SchedulerRow{baseline, treatment})
	}
	keys := make([]string, 0, len(cellPairs))
	for key := range cellPairs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	analysis := SchedulerAnalysis{
		SchemaVersion: schedulerAnalysisSchemaV1, PlanID: planID,
		BootstrapSeed: 424242, BootstrapResamples: 10000,
	}
	for _, key := range keys {
		pairs := cellPairs[key]
		sort.Slice(pairs, func(i, j int) bool {
			if pairs[i][0].Seed != pairs[j][0].Seed {
				return pairs[i][0].Seed < pairs[j][0].Seed
			}
			return pairs[i][0].Repetition < pairs[j][0].Repetition
		})
		cell := SchedulerAnalysisCell{CaseID: pairs[0][0].CaseID, BaselinePolicy: pairs[0][0].Policy, Treatment: pairs[0][1].Policy}
		for metricIndex, spec := range schedulerMetricSpecs(cell.CaseID) {
			baseline, treatment, improvement := pairedMetricValues(pairs, spec)
			lower, upper := bootstrapMedianCI(improvement, analysis.BootstrapResamples, analysis.BootstrapSeed+int64(len(analysis.Cells)*100+metricIndex))
			cell.Metrics = append(cell.Metrics, SchedulerMetricAnalysis{
				Name: spec.name, Direction: spec.direction, Baseline: summarizeDistribution(baseline),
				Treatment: summarizeDistribution(treatment), PairedN: len(improvement),
				ImprovementMedian: nearestRank(improvement, .5), ImprovementCILower: lower,
				ImprovementCIUpper: upper, RankBiserial: pairedRankBiserial(improvement),
			})
		}
		analysis.Cells = append(analysis.Cells, cell)
	}
	return analysis, nil
}

func schedulerMetricSpecs(caseID string) []schedulerMetricSpec {
	lower := func(name string, value func(SchedulerRow) float64) schedulerMetricSpec {
		return schedulerMetricSpec{name: name, direction: "lower-is-better", value: value}
	}
	higher := func(name string, value func(SchedulerRow) float64) schedulerMetricSpec {
		return schedulerMetricSpec{name: name, direction: "higher-is-better", value: value, higherBest: true}
	}
	switch caseID {
	case "S1":
		return []schedulerMetricSpec{
			lower("makespan_ms", func(r SchedulerRow) float64 { return r.MakespanMS }),
			lower("queue_p50_ms", func(r SchedulerRow) float64 { return r.QueueP50MS }),
			lower("queue_p95_ms", func(r SchedulerRow) float64 { return r.QueueP95MS }),
			lower("critical_path_ms", func(r SchedulerRow) float64 { return r.CriticalPathMS }),
			lower("dependency_unlock_ms", func(r SchedulerRow) float64 { return r.DependencyUnlockMS }),
			lower("decision_count", func(r SchedulerRow) float64 { return float64(r.DecisionCount) }),
		}
	case "S2":
		return []schedulerMetricSpec{
			higher("throughput_tasks_s", func(r SchedulerRow) float64 { return r.ThroughputTasksS }),
			higher("jain_fairness", func(r SchedulerRow) float64 { return r.JainFairness }),
			lower("queue_p95_ms", func(r SchedulerRow) float64 { return r.QueueP95MS }),
			lower("admission_rejections", func(r SchedulerRow) float64 { return float64(r.AdmissionRejections) }),
		}
	case "S3":
		return []schedulerMetricSpec{
			lower("critical_path_ms", func(r SchedulerRow) float64 { return r.CriticalPathMS }),
			lower("dependency_unlock_ms", func(r SchedulerRow) float64 { return r.DependencyUnlockMS }),
			lower("starvation_count", func(r SchedulerRow) float64 { return float64(r.StarvationCount) }),
		}
	default:
		return nil
	}
}

func pairedMetricValues(pairs [][2]SchedulerRow, spec schedulerMetricSpec) ([]float64, []float64, []float64) {
	baseline := make([]float64, 0, len(pairs))
	treatment := make([]float64, 0, len(pairs))
	improvement := make([]float64, 0, len(pairs))
	for _, pair := range pairs {
		base, treat := spec.value(pair[0]), spec.value(pair[1])
		baseline, treatment = append(baseline, base), append(treatment, treat)
		difference := base - treat
		if spec.higherBest {
			difference = treat - base
		}
		improvement = append(improvement, difference)
	}
	return baseline, treatment, improvement
}

func summarizeDistribution(values []float64) SchedulerDistribution {
	return SchedulerDistribution{
		N: len(values), Median: nearestRank(values, .5), P50: nearestRank(values, .5),
		P95: nearestRank(values, .95), IQR: nearestRank(values, .75) - nearestRank(values, .25),
	}
}

func bootstrapMedianCI(values []float64, resamples int, seed int64) (float64, float64) {
	random := rand.New(rand.NewSource(seed))
	medians := make([]float64, resamples)
	sample := make([]float64, len(values))
	for index := range medians {
		for draw := range sample {
			sample[draw] = values[random.Intn(len(values))]
		}
		medians[index] = nearestRank(sample, .5)
	}
	return nearestRank(medians, .025), nearestRank(medians, .975)
}

func pairedRankBiserial(values []float64) float64 {
	type ranked struct{ absolute, signed float64 }
	nonzero := make([]ranked, 0, len(values))
	for _, value := range values {
		if value != 0 {
			nonzero = append(nonzero, ranked{absolute: math.Abs(value), signed: value})
		}
	}
	sort.Slice(nonzero, func(i, j int) bool { return nonzero[i].absolute < nonzero[j].absolute })
	var positive, negative float64
	for start := 0; start < len(nonzero); {
		end := start + 1
		for end < len(nonzero) && nonzero[end].absolute == nonzero[start].absolute {
			end++
		}
		rank := (float64(start+1) + float64(end)) / 2
		for index := start; index < end; index++ {
			if nonzero[index].signed > 0 {
				positive += rank
			} else {
				negative += rank
			}
		}
		start = end
	}
	if positive+negative == 0 {
		return 0
	}
	return (positive - negative) / (positive + negative)
}

func validateFormalSchedulerAnalysis(analysis SchedulerAnalysis) error {
	if analysis.SchemaVersion != schedulerAnalysisSchemaV1 || analysis.PlanID != schedulerFormalPlanID ||
		analysis.BootstrapSeed != 424242 || analysis.BootstrapResamples != 10000 || len(analysis.Cells) != 4 {
		return fmt.Errorf("formal scheduler analysis identity is invalid")
	}
	want := map[string]int{"S1/priority": 6, "S1/dag-drcfs": 6, "S2/dag-drcfs": 4, "S3/dag-drcfs": 3}
	for _, cell := range analysis.Cells {
		key := cell.CaseID + "/" + cell.Treatment
		if want[key] != len(cell.Metrics) || cell.BaselinePolicy != baselinePolicy(cell.CaseID) {
			return fmt.Errorf("formal scheduler analysis cell %q is invalid", key)
		}
		delete(want, key)
		for _, metric := range cell.Metrics {
			values := []float64{
				metric.Baseline.Median, metric.Baseline.P50, metric.Baseline.P95, metric.Baseline.IQR,
				metric.Treatment.Median, metric.Treatment.P50, metric.Treatment.P95, metric.Treatment.IQR,
				metric.ImprovementMedian, metric.ImprovementCILower, metric.ImprovementCIUpper, metric.RankBiserial,
			}
			for _, value := range values {
				if math.IsNaN(value) || math.IsInf(value, 0) {
					return fmt.Errorf("formal scheduler metric %q is non-finite", metric.Name)
				}
			}
			if metric.PairedN != 30 || metric.Baseline.N != 30 || metric.Treatment.N != 30 ||
				metric.ImprovementCILower > metric.ImprovementCIUpper || math.Abs(metric.RankBiserial) > 1 {
				return fmt.Errorf("formal scheduler metric %q has invalid paired statistics", metric.Name)
			}
		}
	}
	if len(want) != 0 {
		return fmt.Errorf("formal scheduler analysis cells are incomplete")
	}
	return nil
}
