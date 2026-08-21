package store

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"thermoguard/internal/domain"
)

type Health struct {
	Writable             bool  `json:"writable"`
	AuditValid           bool  `json:"audit_valid"`
	JournalTailTruncated bool  `json:"journal_tail_truncated"`
	Revision             int64 `json:"revision"`
}

var ErrNoChange = errors.New("事务没有产生变化")

type journalEntry struct {
	Revision int64  `json:"revision"`
	Checksum string `json:"checksum"`
	State    State  `json:"state"`
}

type Store struct {
	mu                             sync.RWMutex
	dir, snapshotPath, journalPath string
	state                          State
	tailTruncated                  bool
}

func Open(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("数据目录不能为空")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("创建数据目录: %w", err)
	}
	s := &Store{dir: dir, snapshotPath: filepath.Join(dir, "state.json"), journalPath: filepath.Join(dir, "events.ndjson"), state: NewState()}
	if err := s.load(); err != nil {
		return nil, err
	}
	if err := VerifyAudit(s.state.Audit); err != nil {
		return nil, fmt.Errorf("审计链校验失败: %w", err)
	}
	return s, nil
}

func (s *Store) load() error {
	if data, err := os.ReadFile(s.snapshotPath); err == nil {
		if err := json.Unmarshal(data, &s.state); err != nil {
			return fmt.Errorf("读取快照: %w", err)
		}
		if s.state.SchemaVersion != domain.SchemaVersion {
			return fmt.Errorf("不支持的快照版本 %d", s.state.SchemaVersion)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	s.state.Normalize()
	f, err := os.Open(s.journalPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	reader := bufio.NewReader(f)
	var validBytes int64
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var entry journalEntry
			if err := json.Unmarshal(bytes.TrimSpace(line), &entry); err != nil {
				if readErr == io.EOF {
					s.tailTruncated = true
					if err := f.Close(); err != nil {
						return err
					}
					if err := os.Truncate(s.journalPath, validBytes); err != nil {
						return fmt.Errorf("修复截断日志: %w", err)
					}
					break
				}
				return fmt.Errorf("日志记录损坏: %w", err)
			}
			checksum, err := stateChecksum(entry.State)
			if err != nil || checksum != entry.Checksum {
				return errors.New("日志内容校验失败")
			}
			if entry.State.SchemaVersion != domain.SchemaVersion {
				return fmt.Errorf("不支持的日志快照版本 %d", entry.State.SchemaVersion)
			}
			if entry.State.Revision != entry.Revision {
				return fmt.Errorf("日志修订号不一致: 外层=%d 状态=%d", entry.Revision, entry.State.Revision)
			}
			if entry.Revision > s.state.Revision {
				if entry.Revision != s.state.Revision+1 {
					return fmt.Errorf("日志序号跳跃: %d -> %d", s.state.Revision, entry.Revision)
				}
				s.state = entry.State
				s.state.Normalize()
			}
		}
		if readErr != io.EOF {
			validBytes += int64(len(line))
		} else if len(line) > 0 && line[len(line)-1] != '\n' {
			if err := f.Close(); err != nil {
				return err
			}
			appendFile, err := os.OpenFile(s.journalPath, os.O_WRONLY|os.O_APPEND, 0)
			if err != nil {
				return err
			}
			_, writeErr := appendFile.Write([]byte{'\n'})
			if writeErr == nil {
				writeErr = appendFile.Sync()
			}
			closeErr := appendFile.Close()
			if writeErr != nil {
				return writeErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	return nil
}

func (s *Store) View(fn func(State) error) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	copy, err := cloneState(s.state)
	if err != nil {
		return err
	}
	return fn(copy)
}

func (s *Store) Update(actor, action, objectType, objectID, lotID, before, after string, fn func(*State) error) error {
	if strings.TrimSpace(actor) == "" {
		return domain.Invalid("actor", "行为人不能为空")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next, err := cloneState(s.state)
	if err != nil {
		return err
	}
	if err := fn(&next); err != nil {
		if errors.Is(err, ErrNoChange) {
			return nil
		}
		return err
	}
	if objectID == "pending" {
		objectID = resolveCreatedObjectID(objectType, s.state, next)
	}
	if lotID == "" && objectType == "lot" {
		lotID = objectID
	}
	next.Revision = s.state.Revision + 1
	event := domain.AuditEvent{Sequence: int64(len(next.Audit) + 1), At: time.Now().UTC(), Actor: actor, Action: action, ObjectType: objectType, ObjectID: objectID, LotID: lotID, Before: before, After: after}
	if len(next.Audit) > 0 {
		event.PreviousHash = next.Audit[len(next.Audit)-1].Hash
	}
	event.Hash = AuditHash(event)
	next.Audit = append(next.Audit, event)
	checksum, err := stateChecksum(next)
	if err != nil {
		return err
	}
	entry, err := json.Marshal(journalEntry{Revision: next.Revision, Checksum: checksum, State: next})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.journalPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return err
	}
	if _, err = f.Write(append(entry, '\n')); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := writeAtomic(s.snapshotPath, next); err != nil {
		return err
	}
	s.state = next
	return nil
}

func (s *Store) NextID(state *State, prefix string) string {
	// 外部标识不泄露业务量。熵源异常时仍使用持久化序号保证可用性和唯一性。
	for attempt := 0; attempt < 4; attempt++ {
		var random [12]byte
		if _, err := rand.Read(random[:]); err != nil {
			break
		}
		id := prefix + "-" + hex.EncodeToString(random[:])
		if !idExists(*state, id) {
			return id
		}
	}
	id := fmt.Sprintf("%s-fallback-%016x", prefix, state.NextID)
	state.NextID++
	return id
}

func idExists(state State, id string) bool {
	if _, ok := state.Policies[id]; ok {
		return true
	}
	if _, ok := state.Lots[id]; ok {
		return true
	}
	if _, ok := state.Investigations[id]; ok {
		return true
	}
	if _, ok := state.Actions[id]; ok {
		return true
	}
	for _, items := range state.Readings {
		for _, item := range items {
			if item.ID == id {
				return true
			}
		}
	}
	for _, items := range state.Excursions {
		for _, item := range items {
			if item.ID == id {
				return true
			}
		}
	}
	for _, items := range state.Evidence {
		for _, item := range items {
			if item.ID == id {
				return true
			}
		}
	}
	for _, items := range state.Decisions {
		for _, item := range items {
			if item.ID == id {
				return true
			}
		}
	}
	return false
}

func (s *Store) Health() Health {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Health{Writable: true, AuditValid: VerifyAudit(s.state.Audit) == nil, JournalTailTruncated: s.tailTruncated, Revision: s.state.Revision}
}

func (s *Store) Compact() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := writeAtomic(s.snapshotPath, s.state); err != nil {
		return err
	}
	tmp := s.journalPath + ".tmp"
	if err := os.WriteFile(tmp, nil, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, s.journalPath)
}

func writeAtomic(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".snapshot-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0o640); err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

func cloneState(state State) (State, error) {
	data, err := json.Marshal(state)
	if err != nil {
		return State{}, err
	}
	var out State
	err = json.Unmarshal(data, &out)
	out.Normalize()
	return out, err
}
func stateChecksum(state State) (string, error) {
	data, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func AuditHash(e domain.AuditEvent) string {
	e.Hash = ""
	data, _ := json.Marshal(e)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func VerifyAudit(events []domain.AuditEvent) error {
	previous := ""
	for i, event := range events {
		if event.Sequence != int64(i+1) {
			return fmt.Errorf("审计序号错误: %d", event.Sequence)
		}
		if event.PreviousHash != previous {
			return fmt.Errorf("审计前序哈希错误: %d", event.Sequence)
		}
		if AuditHash(event) != event.Hash {
			return fmt.Errorf("审计事件哈希错误: %d", event.Sequence)
		}
		previous = event.Hash
	}
	return nil
}

func resolveCreatedObjectID(objectType string, before, after State) string {
	switch objectType {
	case "policy":
		for id := range after.Policies {
			if _, exists := before.Policies[id]; !exists {
				return id
			}
		}
	case "lot":
		for id := range after.Lots {
			if _, exists := before.Lots[id]; !exists {
				return id
			}
		}
	case "investigation":
		for id := range after.Investigations {
			if _, exists := before.Investigations[id]; !exists {
				return id
			}
		}
	case "action":
		for id := range after.Actions {
			if _, exists := before.Actions[id]; !exists {
				return id
			}
		}
	case "evidence":
		known := make(map[string]bool)
		for _, items := range before.Evidence {
			for _, item := range items {
				known[item.ID] = true
			}
		}
		for _, items := range after.Evidence {
			for _, item := range items {
				if !known[item.ID] {
					return item.ID
				}
			}
		}
	case "decision":
		known := make(map[string]bool)
		for _, items := range before.Decisions {
			for _, item := range items {
				known[item.ID] = true
			}
		}
		for _, items := range after.Decisions {
			for _, item := range items {
				if !known[item.ID] {
					return item.ID
				}
			}
		}
	}
	return "unknown"
}
