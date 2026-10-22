package benchmark

import (
	"fmt"
	"reflect"
)

const (
	SchedulerProfileSmoke  = "smoke"
	SchedulerProfileFormal = "formal"

	schedulerSmokePlanID  = "s1-single-slot-critical-segment-smoke-v2"
	schedulerFormalPlanID = "scheduler-s1-s2-s3-formal-v1"
)

var schedulerMeasuredSeeds = []uint32{
	104729, 104743, 104759, 104761, 104773, 104779, 104789, 104801, 104803, 104827,
	104831, 104849, 104851, 104869, 104879, 104891, 104911, 104917, 104933, 104947,
	104953, 104959, 104971, 104987, 104999, 105019, 105023, 105031, 105037, 105071,
}

var schedulerWarmupSeeds = []uint32{104701, 104707, 104711}

type schedulerCase struct {
	ID         string
	Treatments []string
	Build      func(uint32) schedulerWorkload
}

type schedulerProfile struct {
	Name          string
	PlanID        string
	BundleVersion string
	WarmupSeeds   []uint32
	Cases         []schedulerCase
}

func NewSchedulerFormalPlan() SchedulerPlan {
	return SchedulerPlan{
		Profile: SchedulerProfileFormal, Seeds: append([]uint32(nil), schedulerMeasuredSeeds...), Repetitions: 1,
	}
}

func resolveSchedulerProfile(plan SchedulerPlan) (schedulerProfile, error) {
	profile := plan.Profile
	if profile == "" {
		profile = SchedulerProfileSmoke
	}
	switch profile {
	case SchedulerProfileSmoke:
		return schedulerProfile{
			Name: profile, PlanID: schedulerSmokePlanID, BundleVersion: s1SmokeShape,
			Cases: []schedulerCase{{ID: "S1-smoke", Treatments: []string{"priority", "dag-drcfs"}, Build: buildS1SmokeWorkload}},
		}, nil
	case SchedulerProfileFormal:
		if plan.Repetitions != 1 || !reflect.DeepEqual(plan.Seeds, schedulerMeasuredSeeds) {
			return schedulerProfile{}, fmt.Errorf("formal scheduler profile uses the fixed 30 seeds and one repetition")
		}
		return schedulerProfile{
			Name: profile, PlanID: schedulerFormalPlanID, BundleVersion: "scheduler-formal-workloads-v1",
			WarmupSeeds: append([]uint32(nil), schedulerWarmupSeeds...),
			Cases: []schedulerCase{
				{ID: "S1", Treatments: []string{"priority", "dag-drcfs"}, Build: buildS1FormalWorkload},
				{ID: "S2", Treatments: []string{"dag-drcfs"}, Build: buildS2FormalWorkload},
				{ID: "S3", Treatments: []string{"dag-drcfs"}, Build: buildS3FormalWorkload},
			},
		}, nil
	default:
		return schedulerProfile{}, fmt.Errorf("scheduler profile must be smoke or formal")
	}
}

func baselinePolicy(caseID string) string {
	if caseID == "S3" {
		return "priority"
	}
	return "fifo"
}
