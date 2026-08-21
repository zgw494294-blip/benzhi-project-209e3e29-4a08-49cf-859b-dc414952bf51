package store

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"thermoguard/internal/domain"
)

const BackupFormat = "thermoguard-backup-v1"

type Backup struct {
	Format    string    `json:"format"`
	CreatedAt time.Time `json:"created_at"`
	Revision  int64     `json:"revision"`
	Checksum  string    `json:"checksum"`
	AuditHead string    `json:"audit_head,omitempty"`
	State     State     `json:"state"`
}

type BackupValidation struct {
	FormatValid   bool   `json:"format_valid"`
	SchemaValid   bool   `json:"schema_valid"`
	ChecksumValid bool   `json:"checksum_valid"`
	AuditValid    bool   `json:"audit_valid"`
	Revision      int64  `json:"revision"`
	AuditHead     string `json:"audit_head,omitempty"`
}

func (s *Store) WriteBackup(writer io.Writer, now time.Time) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state, err := cloneState(s.state)
	if err != nil {
		return err
	}
	if err := VerifyAudit(state.Audit); err != nil {
		return fmt.Errorf("拒绝备份无效审计链: %w", err)
	}
	checksum, err := stateChecksum(state)
	if err != nil {
		return err
	}
	backup := Backup{Format: BackupFormat, CreatedAt: now.UTC(), Revision: state.Revision, Checksum: checksum, State: state}
	if len(state.Audit) > 0 {
		backup.AuditHead = state.Audit[len(state.Audit)-1].Hash
	}
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(backup)
}

func ValidateBackup(reader io.Reader) (BackupValidation, error) {
	var backup Backup
	decoder := json.NewDecoder(io.LimitReader(reader, 64<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&backup); err != nil {
		return BackupValidation{}, fmt.Errorf("读取备份: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return BackupValidation{}, fmt.Errorf("备份只能包含一个 JSON 对象")
	}
	result := BackupValidation{
		FormatValid: backup.Format == BackupFormat, SchemaValid: backup.State.SchemaVersion == domain.SchemaVersion,
		Revision: backup.Revision, AuditHead: backup.AuditHead,
	}
	checksum, err := stateChecksum(backup.State)
	if err != nil {
		return result, err
	}
	result.ChecksumValid = checksum == backup.Checksum && backup.Revision == backup.State.Revision
	result.AuditValid = VerifyAudit(backup.State.Audit) == nil
	if !result.FormatValid || !result.SchemaValid || !result.ChecksumValid || !result.AuditValid {
		return result, fmt.Errorf("备份完整性校验失败")
	}
	if len(backup.State.Audit) > 0 && backup.State.Audit[len(backup.State.Audit)-1].Hash != backup.AuditHead {
		result.AuditValid = false
		return result, fmt.Errorf("备份审计头不匹配")
	}
	return result, nil
}
