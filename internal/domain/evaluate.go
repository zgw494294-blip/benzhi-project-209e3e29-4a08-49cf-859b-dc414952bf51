package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"time"
)

type segment struct {
	kind           ExcursionType
	start, end     time.Time
	peak, exposure float64
}

func Evaluate(policy TemperaturePolicy, lotID string, readings []TemperatureReading) []Excursion {
	items := append([]TemperatureReading(nil), readings...)
	sort.Slice(items, func(i, j int) bool {
		if items[i].SampledAt.Equal(items[j].SampledAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].SampledAt.Before(items[j].SampledAt)
	})
	var segments []segment
	var current *segment
	var total time.Duration
	var totalExposure float64
	var cumulativeStart, cumulativeEnd time.Time
	flush := func() {
		if current != nil {
			segments = append(segments, *current)
			current = nil
		}
	}
	for i, reading := range items {
		if i > 0 {
			gap := reading.SampledAt.Sub(items[i-1].SampledAt)
			if gap > time.Duration(policy.MaxGapMinutes)*time.Minute {
				flush()
				segments = append(segments, segment{kind: ExcursionGap, start: items[i-1].SampledAt, end: reading.SampledAt, peak: float64(gap / time.Minute)})
			}
		}
		kind := ExcursionType("")
		delta := 0.0
		if reading.Celsius > policy.MaxC {
			kind, delta = ExcursionHigh, reading.Celsius-policy.MaxC
		}
		if reading.Celsius < policy.MinC {
			kind, delta = ExcursionLow, policy.MinC-reading.Celsius
		}
		if kind == "" {
			flush()
			continue
		}
		if current == nil || current.kind != kind || (i > 0 && reading.SampledAt.Sub(items[i-1].SampledAt) > time.Duration(policy.MaxGapMinutes)*time.Minute) {
			flush()
			current = &segment{kind: kind, start: reading.SampledAt, end: reading.SampledAt, peak: reading.Celsius}
		} else {
			d := reading.SampledAt.Sub(current.end)
			current.exposure += delta * d.Minutes()
			current.end = reading.SampledAt
			if kind == ExcursionHigh {
				current.peak = math.Max(current.peak, reading.Celsius)
			} else {
				current.peak = math.Min(current.peak, reading.Celsius)
			}
		}
	}
	flush()
	result := make([]Excursion, 0, len(segments)+1)
	for _, s := range segments {
		duration := s.end.Sub(s.start)
		if s.kind != ExcursionGap {
			total += duration
			totalExposure += s.exposure
			if duration > 0 {
				if cumulativeStart.IsZero() {
					cumulativeStart = s.start
				}
				cumulativeEnd = s.end
			}
			if duration < time.Duration(policy.ContinuousMinutes)*time.Minute {
				continue
			}
		}
		result = append(result, excursionFromSegment(policy, lotID, s))
	}
	if total >= time.Duration(policy.CumulativeMinutes)*time.Minute {
		s := segment{kind: ExcursionCumulative, start: cumulativeStart, end: cumulativeEnd, peak: total.Minutes(), exposure: totalExposure}
		result = append(result, excursionFromSegment(policy, lotID, s))
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].StartAt.Equal(result[j].StartAt) {
			return result[i].Type < result[j].Type
		}
		return result[i].StartAt.Before(result[j].StartAt)
	})
	return result
}

func excursionFromSegment(p TemperaturePolicy, lotID string, s segment) Excursion {
	duration := s.end.Sub(s.start).Minutes()
	delta := 0.0
	switch s.kind {
	case ExcursionHigh:
		delta = s.peak - p.MaxC
	case ExcursionLow:
		delta = p.MinC - s.peak
	case ExcursionGap:
		duration = s.end.Sub(s.start).Minutes()
		delta = duration / float64(p.MaxGapMinutes)
	case ExcursionCumulative:
		duration = s.peak
		delta = s.exposure / math.Max(duration, 1)
	}
	severity := SeverityMinor
	if delta >= p.CriticalDeltaC || duration >= float64(p.ContinuousMinutes*4) {
		severity = SeverityCritical
	} else if delta >= p.MajorDeltaC || duration >= float64(p.ContinuousMinutes*2) {
		severity = SeverityMajor
	}
	fpInput := fmt.Sprintf("%s|%s|%s|%s", lotID, s.kind, s.start.UTC().Format(time.RFC3339Nano), s.end.UTC().Format(time.RFC3339Nano))
	sum := sha256.Sum256([]byte(fpInput))
	return Excursion{LotID: lotID, Fingerprint: hex.EncodeToString(sum[:12]), Type: s.kind, Severity: severity, StartAt: s.start, EndAt: s.end, DurationMinutes: duration, PeakC: s.peak, ExposureDegreeMinutes: s.exposure, Basis: fmt.Sprintf("规则 %s v%d", p.ID, p.Version), Active: true}
}
