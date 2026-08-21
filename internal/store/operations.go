package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

type Statistics struct {
	Revision       int64  `json:"revision"`
	Policies       int    `json:"policies"`
	Lots           int    `json:"lots"`
	Readings       int    `json:"readings"`
	Excursions     int    `json:"excursions"`
	Investigations int    `json:"investigations"`
	Evidence       int    `json:"evidence"`
	Actions        int    `json:"actions"`
	Decisions      int    `json:"decisions"`
	AuditEvents    int    `json:"audit_events"`
	AuditHead      string `json:"audit_head,omitempty"`
	SnapshotBytes  int64  `json:"snapshot_bytes"`
	JournalBytes   int64  `json:"journal_bytes"`
}

func (s *Store) Statistics() (Statistics, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := Statistics{
		Revision: s.state.Revision, Policies: len(s.state.Policies), Lots: len(s.state.Lots),
		Investigations: len(s.state.Investigations), Actions: len(s.state.Actions),
		AuditEvents: len(s.state.Audit),
	}
	for _, readings := range s.state.Readings {
		result.Readings += len(readings)
	}
	for _, excursions := range s.state.Excursions {
		result.Excursions += len(excursions)
	}
	for _, evidence := range s.state.Evidence {
		result.Evidence += len(evidence)
	}
	for _, decisions := range s.state.Decisions {
		result.Decisions += len(decisions)
	}
	if len(s.state.Audit) > 0 {
		result.AuditHead = s.state.Audit[len(s.state.Audit)-1].Hash
	}
	if info, err := os.Stat(s.snapshotPath); err == nil {
		result.SnapshotBytes = info.Size()
	} else if !os.IsNotExist(err) {
		return result, err
	}
	if info, err := os.Stat(s.journalPath); err == nil {
		result.JournalBytes = info.Size()
	} else if !os.IsNotExist(err) {
		return result, err
	}
	return result, nil
}

type JournalInspection struct {
	Entries          int    `json:"entries"`
	FirstRevision    int64  `json:"first_revision"`
	LastRevision     int64  `json:"last_revision"`
	LastChecksum     string `json:"last_checksum,omitempty"`
	SequenceValid    bool   `json:"sequence_valid"`
	ChecksumsValid   bool   `json:"checksums_valid"`
	TruncatedTail    bool   `json:"truncated_tail"`
	InvalidEntryLine int    `json:"invalid_entry_line,omitempty"`
}

func (s *Store) InspectJournal() (JournalInspection, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := JournalInspection{SequenceValid: true, ChecksumsValid: true}
	f, err := os.Open(s.journalPath)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	defer f.Close()
	reader := bufio.NewReader(f)
	lineNumber := 0
	for {
		line, readErr := reader.ReadBytes('\n')
		lineNumber++
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) > 0 {
			var entry journalEntry
			if err := json.Unmarshal(trimmed, &entry); err != nil {
				if readErr == io.EOF {
					result.TruncatedTail = true
					break
				}
				result.InvalidEntryLine = lineNumber
				return result, fmt.Errorf("日志第 %d 行无效: %w", lineNumber, err)
			}
			checksum, err := stateChecksum(entry.State)
			if err != nil || checksum != entry.Checksum {
				result.ChecksumsValid = false
				result.InvalidEntryLine = lineNumber
				return result, fmt.Errorf("日志第 %d 行校验和无效", lineNumber)
			}
			if result.Entries == 0 {
				result.FirstRevision = entry.Revision
			} else if entry.Revision != result.LastRevision+1 {
				result.SequenceValid = false
				result.InvalidEntryLine = lineNumber
				return result, fmt.Errorf("日志第 %d 行序号不连续", lineNumber)
			}
			result.Entries++
			result.LastRevision = entry.Revision
			result.LastChecksum = entry.Checksum
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return result, readErr
		}
	}
	return result, nil
}

func (s *Store) MaybeCompact(maxEntries int, maxBytes int64) (bool, error) {
	if maxEntries <= 0 && maxBytes <= 0 {
		return false, fmt.Errorf("至少提供一个压缩阈值")
	}
	inspection, err := s.InspectJournal()
	if err != nil {
		return false, err
	}
	info, statErr := os.Stat(s.journalPath)
	if statErr != nil && !os.IsNotExist(statErr) {
		return false, statErr
	}
	size := int64(0)
	if statErr == nil {
		size = info.Size()
	}
	byEntries := maxEntries > 0 && inspection.Entries >= maxEntries
	bySize := maxBytes > 0 && size >= maxBytes
	if !byEntries && !bySize {
		return false, nil
	}
	if err := s.Compact(); err != nil {
		return false, err
	}
	return true, nil
}
