package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/xlyk/triptych/internal/domain"
)

type CommandReceiptStore interface {
	HasApplied(domain.CommandID) (bool, error)
	MarkApplied(PendingCommand) error
	Clear(domain.CommandID) error
}

type LocalCommandReceiptStore struct {
	baseDir string
}

type commandReceipt struct {
	CommandID   domain.CommandID   `json:"command_id"`
	RunID       domain.RunID       `json:"run_id"`
	CommandType domain.CommandType `json:"command_type"`
	AppliedAt   time.Time          `json:"applied_at"`
}

func NewLocalCommandReceiptStore(baseDir string) *LocalCommandReceiptStore {
	return &LocalCommandReceiptStore{baseDir: baseDir}
}

func (s *LocalCommandReceiptStore) HasApplied(commandID domain.CommandID) (bool, error) {
	_, err := os.Stat(s.receiptPath(commandID))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("stat command receipt %s: %w", commandID, err)
}

func (s *LocalCommandReceiptStore) MarkApplied(cmd PendingCommand) error {
	if err := os.MkdirAll(s.baseDir, 0o755); err != nil {
		return fmt.Errorf("create receipt dir: %w", err)
	}
	receipt := commandReceipt{
		CommandID:   cmd.CommandID,
		RunID:       cmd.RunID,
		CommandType: cmd.CommandType,
		AppliedAt:   time.Now().UTC(),
	}
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal command receipt %s: %w", cmd.CommandID, err)
	}
	tmpPath := s.receiptPath(cmd.CommandID) + ".tmp"
	if err := os.WriteFile(tmpPath, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write command receipt %s: %w", cmd.CommandID, err)
	}
	if err := os.Rename(tmpPath, s.receiptPath(cmd.CommandID)); err != nil {
		return fmt.Errorf("commit command receipt %s: %w", cmd.CommandID, err)
	}
	return nil
}

func (s *LocalCommandReceiptStore) Clear(commandID domain.CommandID) error {
	err := os.Remove(s.receiptPath(commandID))
	if err == nil || os.IsNotExist(err) {
		return nil
	}
	return fmt.Errorf("remove command receipt %s: %w", commandID, err)
}

func (s *LocalCommandReceiptStore) receiptPath(commandID domain.CommandID) string {
	return filepath.Join(s.baseDir, commandID.String()+".json")
}
