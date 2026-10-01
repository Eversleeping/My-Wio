package agent

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

const (
	completedOperationLimit     = 128
	completedOperationByteLimit = 16 << 20
	completedOperationTTL       = time.Hour
)

// Called under seenMu. Active operations are never evicted: reconnect retries
// must wait for their original execution instead of launching another copy.
func (c *Client) pruneCompletedOperations(now time.Time) {
	count, bytes := 0, 0
	for id, execution := range c.seen {
		if execution.completedAt.IsZero() || !execution.persisted {
			continue
		}
		if now.Sub(execution.completedAt) > completedOperationTTL {
			delete(c.seen, id)
			continue
		}
		count++
		bytes += len(execution.result.Data) + len(execution.result.Message)
	}
	for count > completedOperationLimit || bytes > completedOperationByteLimit {
		oldestID := ""
		var oldest time.Time
		for id, execution := range c.seen {
			if execution.persisted && !execution.completedAt.IsZero() && (oldestID == "" || execution.completedAt.Before(oldest)) {
				oldestID, oldest = id, execution.completedAt
			}
		}
		if oldestID == "" {
			break
		}
		execution := c.seen[oldestID]
		bytes -= len(execution.result.Data) + len(execution.result.Message)
		delete(c.seen, oldestID)
		count--
	}
}

// Completed receipts are compressed on disk so evicting their RAM cache does
// not replay Git writes or deployments on a delayed reconnect or Agent restart.
func (c *Client) operationResultPath(id string) string {
	digest := sha256.Sum256([]byte(id))
	return filepath.Join(c.config.StateDir, "operation-results", hex.EncodeToString(digest[:])+".json.gz")
}

func (c *Client) saveOperationResult(result protocol.OperationResult) error {
	if c.config.StateDir == "" {
		return nil
	}
	path := c.operationResultPath(result.OperationID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".receipt-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	writer := gzip.NewWriter(file)
	encodeErr := json.NewEncoder(writer).Encode(result)
	zipErr := writer.Close()
	closeErr := file.Close()
	if encodeErr != nil {
		return encodeErr
	}
	if zipErr != nil {
		return zipErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), path)
}

func (c *Client) loadOperationResult(id string) (protocol.OperationResult, bool, error) {
	var result protocol.OperationResult
	if c.config.StateDir == "" {
		return result, false, nil
	}
	file, err := os.Open(c.operationResultPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return result, false, nil
	}
	if err != nil {
		return result, false, err
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return result, true, err
	}
	defer reader.Close()
	err = json.NewDecoder(reader).Decode(&result)
	if err == nil {
		_, err = io.Copy(io.Discard, reader) // Verify gzip checksum before replay.
	}
	if err == nil && result.OperationID != id {
		err = errors.New("operation receipt ID mismatch")
	}
	return result, true, err
}
