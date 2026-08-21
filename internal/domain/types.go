package domain

import "time"

const SchemaVersion = 1

type PolicyStatus string

const (
	PolicyDraft     PolicyStatus = "DRAFT"
	PolicyPublished PolicyStatus = "PUBLISHED"
)

type Severity string

const (
	SeverityMinor    Severity = "MINOR"
	SeverityMajor    Severity = "MAJOR"
	SeverityCritical Severity = "CRITICAL"
)

type TemperaturePolicy struct {
	ID                string       `json:"id"`
	Name              string       `json:"name"`
	Version           int          `json:"version"`
	Status            PolicyStatus `json:"status"`
	MinC              float64      `json:"min_c"`
	MaxC              float64      `json:"max_c"`
	MaxGapMinutes     int          `json:"max_gap_minutes"`
	ContinuousMinutes int          `json:"continuous_minutes"`
	CumulativeMinutes int          `json:"cumulative_minutes"`
	MajorDeltaC       float64      `json:"major_delta_c"`
	CriticalDeltaC    float64      `json:"critical_delta_c"`
	CreatedAt         time.Time    `json:"created_at"`
	PublishedAt       *time.Time   `json:"published_at,omitempty"`
}

type LotStatus string

const (
	LotDraft       LotStatus = "DRAFT"
	LotMonitoring  LotStatus = "MONITORING"
	LotUnderReview LotStatus = "UNDER_REVIEW"
	LotReleased    LotStatus = "RELEASED"
	LotRejected    LotStatus = "REJECTED"
)

type MonitoredLot struct {
	ID                string    `json:"id"`
	Code              string    `json:"code"`
	Product           string    `json:"product"`
	PolicyID          string    `json:"policy_id"`
	StartAt           time.Time `json:"start_at"`
	EndAt             time.Time `json:"end_at"`
	Status            LotStatus `json:"status"`
	Version           int64     `json:"version"`
	EvaluationVersion int64     `json:"evaluation_version"`
	CreatedAt         time.Time `json:"created_at"`
}

type TemperatureReading struct {
	ID             string    `json:"id"`
	LotID          string    `json:"lot_id"`
	DeviceID       string    `json:"device_id"`
	SampledAt      time.Time `json:"sampled_at"`
	Celsius        float64   `json:"celsius"`
	Source         string    `json:"source"`
	IdempotencyKey string    `json:"idempotency_key"`
	CreatedAt      time.Time `json:"created_at"`
}

type ExcursionType string

const (
	ExcursionHigh       ExcursionType = "HIGH"
	ExcursionLow        ExcursionType = "LOW"
	ExcursionGap        ExcursionType = "GAP"
	ExcursionCumulative ExcursionType = "CUMULATIVE"
)

type Excursion struct {
	ID                    string        `json:"id"`
	LotID                 string        `json:"lot_id"`
	Fingerprint           string        `json:"fingerprint"`
	Type                  ExcursionType `json:"type"`
	Severity              Severity      `json:"severity"`
	StartAt               time.Time     `json:"start_at"`
	EndAt                 time.Time     `json:"end_at"`
	DurationMinutes       float64       `json:"duration_minutes"`
	PeakC                 float64       `json:"peak_c"`
	ExposureDegreeMinutes float64       `json:"exposure_degree_minutes"`
	Basis                 string        `json:"basis"`
	Active                bool          `json:"active"`
	RevokedAt             *time.Time    `json:"revoked_at,omitempty"`
}

type InvestigationStatus string

const (
	InvestigationOpen      InvestigationStatus = "OPEN"
	InvestigationSubmitted InvestigationStatus = "SUBMITTED"
	InvestigationVoid      InvestigationStatus = "VOID"
)

type Investigation struct {
	ID           string              `json:"id"`
	LotID        string              `json:"lot_id"`
	ExcursionIDs []string            `json:"excursion_ids"`
	Status       InvestigationStatus `json:"status"`
	RootCause    string              `json:"root_cause,omitempty"`
	Impact       string              `json:"impact,omitempty"`
	Summary      string              `json:"summary,omitempty"`
	CreatedAt    time.Time           `json:"created_at"`
	SubmittedAt  *time.Time          `json:"submitted_at,omitempty"`
}

type Evidence struct {
	ID              string    `json:"id"`
	InvestigationID string    `json:"investigation_id"`
	Category        string    `json:"category"`
	Reference       string    `json:"reference"`
	Collector       string    `json:"collector"`
	Summary         string    `json:"summary"`
	CollectedAt     time.Time `json:"collected_at"`
}

type ActionStatus string

const (
	ActionOpen      ActionStatus = "OPEN"
	ActionCompleted ActionStatus = "COMPLETED"
	ActionCancelled ActionStatus = "CANCELLED"
)

type CAPAAction struct {
	ID              string       `json:"id"`
	InvestigationID string       `json:"investigation_id"`
	Type            string       `json:"type"`
	Description     string       `json:"description"`
	Owner           string       `json:"owner"`
	DueAt           time.Time    `json:"due_at"`
	Status          ActionStatus `json:"status"`
	CompletionNote  string       `json:"completion_note,omitempty"`
	CompletedAt     *time.Time   `json:"completed_at,omitempty"`
	OverdueNotified bool         `json:"overdue_notified"`
}

type DecisionType string

const (
	DecisionRelease DecisionType = "RELEASE"
	DecisionHold    DecisionType = "HOLD"
	DecisionReject  DecisionType = "REJECT"
)

type ReleaseDecision struct {
	ID              string       `json:"id"`
	LotID           string       `json:"lot_id"`
	Decision        DecisionType `json:"decision"`
	Reason          string       `json:"reason"`
	ExpectedVersion int64        `json:"expected_version"`
	Blockers        []Blocker    `json:"blockers"`
	Actor           string       `json:"actor"`
	CreatedAt       time.Time    `json:"created_at"`
}

type Blocker struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	ObjectID string `json:"object_id,omitempty"`
}
type ReleasePreview struct {
	LotID     string       `json:"lot_id"`
	Version   int64        `json:"version"`
	Suggested DecisionType `json:"suggested"`
	Blockers  []Blocker    `json:"blockers"`
}

type AuditEvent struct {
	Sequence     int64     `json:"sequence"`
	At           time.Time `json:"at"`
	Actor        string    `json:"actor"`
	Action       string    `json:"action"`
	ObjectType   string    `json:"object_type"`
	ObjectID     string    `json:"object_id"`
	LotID        string    `json:"lot_id,omitempty"`
	Before       string    `json:"before,omitempty"`
	After        string    `json:"after,omitempty"`
	PreviousHash string    `json:"previous_hash"`
	Hash         string    `json:"hash"`
}

type IdempotencyRecord struct {
	Scope    string `json:"scope"`
	Key      string `json:"key"`
	Digest   string `json:"digest"`
	ResultID string `json:"result_id"`
}
