package domain

import (
	"math"
	"strings"
	"time"
)

func ValidatePolicy(p TemperaturePolicy) error {
	if strings.TrimSpace(p.Name) == "" {
		return Invalid("name", "规则名称不能为空")
	}
	if math.IsNaN(p.MinC) || math.IsNaN(p.MaxC) || math.IsInf(p.MinC, 0) || math.IsInf(p.MaxC, 0) || p.MinC < -100 || p.MaxC > 100 || p.MinC >= p.MaxC {
		return Invalid("temperature_range", "允许温区无效")
	}
	if p.MaxGapMinutes <= 0 || p.ContinuousMinutes <= 0 || p.CumulativeMinutes <= 0 {
		return Invalid("thresholds", "时间阈值必须为正整数")
	}
	if p.MajorDeltaC <= 0 || p.CriticalDeltaC <= p.MajorDeltaC {
		return Invalid("severity_matrix", "严重等级温差矩阵无效")
	}
	return nil
}

func ValidateLot(l MonitoredLot) error {
	if strings.TrimSpace(l.Code) == "" {
		return Invalid("code", "批次编码不能为空")
	}
	if strings.TrimSpace(l.Product) == "" {
		return Invalid("product", "产品名称不能为空")
	}
	if l.StartAt.IsZero() || l.EndAt.IsZero() || l.StartAt.Location() != time.UTC || l.EndAt.Location() != time.UTC {
		return Invalid("monitoring_window", "监测时间窗必须使用 RFC3339 UTC 时间")
	}
	if !l.EndAt.After(l.StartAt) {
		return Invalid("end_at", "监测结束时间必须晚于开始时间")
	}
	return nil
}

func ValidateReading(l MonitoredLot, r TemperatureReading) error {
	if strings.TrimSpace(r.DeviceID) == "" {
		return Invalid("device_id", "设备编号不能为空")
	}
	if strings.TrimSpace(r.Source) == "" {
		return Invalid("source", "读数来源不能为空")
	}
	if r.IdempotencyKey == "" {
		return Invalid("idempotency_key", "幂等键不能为空")
	}
	if len(r.IdempotencyKey) > 128 {
		return Invalid("idempotency_key", "幂等键过长")
	}
	if math.IsNaN(r.Celsius) || math.IsInf(r.Celsius, 0) || r.Celsius < -100 || r.Celsius > 100 {
		return Invalid("celsius", "温度必须位于 -100 到 100 摄氏度")
	}
	if r.SampledAt.IsZero() || r.SampledAt.Location() != time.UTC {
		return Invalid("sampled_at", "sampled_at 必须是 RFC3339 UTC 时间")
	}
	if r.SampledAt.Before(l.StartAt) || r.SampledAt.After(l.EndAt) {
		return Invalid("sampled_at", "采样时间不在批次监测时间窗内")
	}
	return nil
}

func ParseUTC(value, field string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil || t.Location() != time.UTC {
		return time.Time{}, Invalid(field, field+" 必须是 RFC3339 UTC 时间")
	}
	return t.UTC(), nil
}
