package domain

import (
	"testing"
	"time"
)

func TestEvaluateBoundariesAndGap(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	policy := TemperaturePolicy{ID: "p", Version: 1, MinC: 2, MaxC: 8, MaxGapMinutes: 20, ContinuousMinutes: 10, CumulativeMinutes: 20, MajorDeltaC: 2, CriticalDeltaC: 5}
	readings := []TemperatureReading{
		{ID: "r3", SampledAt: base.Add(50 * time.Minute), Celsius: 8},
		{ID: "r1", SampledAt: base, Celsius: 8},
		{ID: "r2", SampledAt: base.Add(15 * time.Minute), Celsius: 11},
	}
	out := Evaluate(policy, "lot", readings)
	if len(out) != 1 || out[0].Type != ExcursionGap {
		t.Fatalf("期望只形成读数缺口，得到 %#v", out)
	}
}

func TestEvaluateThresholdReachedAndStable(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	policy := TemperaturePolicy{ID: "p", Version: 2, MinC: 2, MaxC: 8, MaxGapMinutes: 30, ContinuousMinutes: 10, CumulativeMinutes: 10, MajorDeltaC: 2, CriticalDeltaC: 4}
	readings := []TemperatureReading{{ID: "b", SampledAt: base.Add(10 * time.Minute), Celsius: 13}, {ID: "a", SampledAt: base, Celsius: 12}}
	first := Evaluate(policy, "lot", readings)
	second := Evaluate(policy, "lot", []TemperatureReading{readings[1], readings[0]})
	if len(first) != 2 {
		t.Fatalf("阈值达到时应形成严重高温及累计偏差: %#v", first)
	}
	byType := map[ExcursionType]Excursion{}
	for _, item := range first {
		byType[item.Type] = item
	}
	if byType[ExcursionHigh].Severity != SeverityCritical {
		t.Fatalf("高温严重等级错误: %#v", byType[ExcursionHigh])
	}
	secondByType := map[ExcursionType]Excursion{}
	for _, item := range second {
		secondByType[item.Type] = item
	}
	for kind, item := range byType {
		if item.Fingerprint != secondByType[kind].Fingerprint {
			t.Fatalf("乱序输入改变了 %s 稳定指纹", kind)
		}
	}
}

func TestEvaluateCumulativeIncludesShortSegments(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	policy := TemperaturePolicy{ID: "p", Version: 1, MinC: 2, MaxC: 8, MaxGapMinutes: 30, ContinuousMinutes: 10, CumulativeMinutes: 12, MajorDeltaC: 2, CriticalDeltaC: 5}
	readings := []TemperatureReading{
		{ID: "r1", SampledAt: base, Celsius: 9},
		{ID: "r2", SampledAt: base.Add(6 * time.Minute), Celsius: 9},
		{ID: "r3", SampledAt: base.Add(7 * time.Minute), Celsius: 8},
		{ID: "r4", SampledAt: base.Add(10 * time.Minute), Celsius: 9},
		{ID: "r5", SampledAt: base.Add(16 * time.Minute), Celsius: 9},
	}
	out := Evaluate(policy, "lot", readings)
	if len(out) != 1 || out[0].Type != ExcursionCumulative || out[0].DurationMinutes != 12 {
		t.Fatalf("短时超限片段应累计形成 12 分钟偏差: %#v", out)
	}
}

func TestCumulativeFingerprintIgnoresNormalPadding(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	policy := TemperaturePolicy{ID: "p", Version: 1, MinC: 2, MaxC: 8, MaxGapMinutes: 30, ContinuousMinutes: 10, CumulativeMinutes: 12, MajorDeltaC: 2, CriticalDeltaC: 5}
	exposure := []TemperatureReading{
		{ID: "r1", SampledAt: base.Add(10 * time.Minute), Celsius: 9},
		{ID: "r2", SampledAt: base.Add(16 * time.Minute), Celsius: 9},
		{ID: "r3", SampledAt: base.Add(17 * time.Minute), Celsius: 8},
		{ID: "r4", SampledAt: base.Add(20 * time.Minute), Celsius: 9},
		{ID: "r5", SampledAt: base.Add(26 * time.Minute), Celsius: 9},
	}
	first := Evaluate(policy, "lot", exposure)
	padded := append([]TemperatureReading{{ID: "normal-before", SampledAt: base, Celsius: 5}}, exposure...)
	padded = append(padded, TemperatureReading{ID: "normal-after", SampledAt: base.Add(30 * time.Minute), Celsius: 5})
	second := Evaluate(policy, "lot", padded)
	if len(first) != 1 || len(second) != 1 || first[0].Type != ExcursionCumulative || second[0].Type != ExcursionCumulative {
		t.Fatalf("期望只形成累计偏差: first=%#v second=%#v", first, second)
	}
	if first[0].Fingerprint != second[0].Fingerprint {
		t.Fatalf("正常边界读数改变了累计偏差指纹: %s != %s", first[0].Fingerprint, second[0].Fingerprint)
	}
}
