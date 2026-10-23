package codexadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type forkHistoryTurn struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// Older Codex releases ignore lastTurnId. Verify the copied native history and
// trim only the new thread when necessary, so UI history and model context agree.
func trimForkToTurn(ctx context.Context, threadID, lastTurnID string, fork json.RawMessage, request requestFunc) error {
	var response struct {
		Thread struct {
			Turns []forkHistoryTurn `json:"turns"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(fork, &response); err != nil {
		return err
	}
	if len(response.Thread.Turns) > 0 {
		return trimLegacyFork(ctx, threadID, lastTurnID, response.Thread.Turns, request)
	}
	cursor := ""
	seen := make(map[string]bool)
	seenTurns := make(map[string]bool)
	var count uint32
	for {
		params := map[string]any{"threadId": threadID, "limit": uint32(100), "sortDirection": "desc", "itemsView": "notLoaded"}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := request(ctx, "thread/turns/list", params)
		if err != nil {
			var rpcErr *rpcRequestError
			if cursor != "" || !errors.As(err, &rpcErr) || !(rpcErr.Code == -32601 || (rpcErr.Code == -32600 && strings.Contains(rpcErr.Message, "unknown variant `thread/turns/list`"))) {
				return err
			}
			raw, err = request(ctx, "thread/read", map[string]any{"threadId": threadID, "includeTurns": true})
			if err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &response); err != nil {
				return err
			}
			return trimLegacyFork(ctx, threadID, lastTurnID, response.Thread.Turns, request)
		}
		var page struct {
			Data       []forkHistoryTurn `json:"data"`
			NextCursor string            `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return fmt.Errorf("invalid fork history: %w", err)
		}
		for _, turn := range page.Data {
			if turn.ID == "" || seenTurns[turn.ID] {
				return errors.New("fork history contains an invalid or repeated turn")
			}
			seenTurns[turn.ID] = true
			if turn.ID == lastTurnID {
				if turn.Status == "inProgress" {
					return errors.New("cannot branch from an unfinished turn")
				}
				if count > 0 {
					return revertThreadTurns(ctx, threadID, count, request)
				}
				return nil
			}
			count++
		}
		if page.NextCursor == "" || seen[page.NextCursor] || len(page.Data) == 0 {
			return errors.New("could not locate the requested fork boundary in Codex history")
		}
		cursor = page.NextCursor
		seen[cursor] = true
	}
}

func trimLegacyFork(ctx context.Context, threadID, lastTurnID string, turns []forkHistoryTurn, request requestFunc) error {
	for index, turn := range turns {
		if turn.ID != lastTurnID {
			continue
		}
		if turn.Status == "inProgress" {
			return errors.New("cannot branch from an unfinished turn")
		}
		count := uint32(len(turns) - index - 1)
		if count > 0 {
			return revertThreadTurns(ctx, threadID, count, request)
		}
		return nil
	}
	return errors.New("could not locate the requested fork boundary in Codex history")
}
