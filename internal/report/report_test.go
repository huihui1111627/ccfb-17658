package report

import (
	"testing"

	"baremetal-rollout/internal/fleet"
)

func TestComparisonBatchedPerfectAttribution(t *testing.T) {
	base := fleet.Generate(800, 20261127)
	comp, err := RunComparison(base, 20261127)
	if err != nil {
		t.Fatal(err)
	}

	if comp.BatchAccuracy.Matched != 800 || comp.BatchAccuracy.Accuracy != 1 {
		t.Fatalf("batch accuracy = %d/%d %.4f", comp.BatchAccuracy.Matched, comp.BatchAccuracy.Total, comp.BatchAccuracy.Accuracy)
	}
	if comp.Batched.Success != 564 || comp.Batched.Failed != 0 || comp.Batched.Blocked != 236 {
		t.Fatalf("batched outcomes = success %d blocked %d failed %d", comp.Batched.Success, comp.Batched.Blocked, comp.Batched.Failed)
	}
	if comp.Storm.Success < 380 || comp.Storm.Success > 470 {
		t.Fatalf("storm success = %d, want calibrated partial success around 430", comp.Storm.Success)
	}
	if comp.Storm.Failed < 80 || comp.Storm.Failed > 220 {
		t.Fatalf("storm overload failures = %d, want a visible but not total collapse", comp.Storm.Failed)
	}
	if comp.SuccessGain <= 0 || comp.StormExtraFailures <= 0 {
		t.Fatalf("batch should improve success and failures: gain=%d extraFailures=%d", comp.SuccessGain, comp.StormExtraFailures)
	}
	if len(comp.Batched.Clusters) != 2 {
		t.Fatalf("rack clusters = %d, want 2 faulty ToR racks", len(comp.Batched.Clusters))
	}
	if comp.Plan.BatchSize <= 0 || comp.Plan.BatchCount != 12 {
		t.Fatalf("plan = size %d count %d", comp.Plan.BatchSize, comp.Plan.BatchCount)
	}
}
