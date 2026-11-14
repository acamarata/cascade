// Package background provides sub-spawn tooling.
package background

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrRoleRequired is returned when the Role field is empty or whitespace.
var ErrRoleRequired = errors.New("role is required")

// ErrInvalidSubSpawn is returned when a SubSpawnRequest fails validation.
var ErrInvalidSubSpawn = errors.New("invalid sub_spawn request")

// ErrSubChat is returned when the sub-chat identifier is malformed or Open fails.
var ErrSubChat = errors.New("sub-chat failed")

// SubSpawnRequest describes a request to spawn a sub-task.
type SubSpawnRequest struct {
	Task  string
	Role  string
	Scope string
	Async bool
}

// SubChatRequest describes a request to open a sub-chat.
type SubChatRequest struct {
	Task     string
	Role     string
	Scope    string
	Metadata map[string]string
}

// SubSpawnReport holds the result of a sub-spawn operation.
type SubSpawnReport struct {
	ID       string
	Handle   string
	Status   string
	Report   string
	Metadata map[string]string
}

// SubChats defines the interface for opening and waiting on sub-chats.
type SubChats interface {
	// Open starts a new sub-chat and returns its identifier.
	Open(ctx context.Context, req SubChatRequest) (string, error)
	// Wait blocks until the sub-chat identified by id completes.
	Wait(ctx context.Context, id string) (string, error)
}

// SubSpawnTool is a function that executes a sub-spawn request.
type SubSpawnTool func(context.Context, SubSpawnRequest) (SubSpawnReport, error)

// SubSpawnRegistry maps tool names to their implementations.
type SubSpawnRegistry map[string]SubSpawnTool

// SubSpawn validates the inputs, opens a scoped sub-chat, and returns a report.
func SubSpawn(ctx context.Context, chats SubChats, registry SubSpawnRegistry, req SubSpawnRequest) (SubSpawnReport, error) {
	if err := validateSubSpawn(ctx, chats, req); err != nil {
		return SubSpawnReport{}, err
	}

	id, err := chats.Open(ctx, SubChatRequest{
		Task:     req.Task,
		Role:     req.Role,
		Scope:    req.Scope,
		Metadata: subSpawnMetadata(),
	})
	if err != nil {
		return SubSpawnReport{}, fmt.Errorf("%w: %v", ErrSubChat, err)
	}

	if !isValidSubChatID(id) {
		return SubSpawnReport{}, ErrSubChat
	}

	if registry != nil {
		registry["sub_spawn"] = func(c context.Context, r SubSpawnRequest) (SubSpawnReport, error) {
			return SubSpawn(c, chats, registry, r)
		}
	}

	if req.Async {
		return SubSpawnReport{
			ID:     id,
			Handle: id,
			Status: "pending",
			Report: "",
		}, nil
	}

	report, err := chats.Wait(ctx, id)
	if err != nil {
		return SubSpawnReport{}, fmt.Errorf("%w: %v", ErrSubChat, err)
	}

	return SubSpawnReport{
		ID:     id,
		Handle: id,
		Status: "completed",
		Report: report,
	}, nil
}

// validateSubSpawn checks the SubSpawn inputs in the contract's order.
func validateSubSpawn(ctx context.Context, chats SubChats, req SubSpawnRequest) error {
	if ctx == nil {
		return ErrInvalidSubSpawn
	}
	if chats == nil {
		return ErrInvalidSubSpawn
	}
	if strings.TrimSpace(req.Task) == "" {
		return ErrInvalidSubSpawn
	}
	if strings.TrimSpace(req.Role) == "" {
		return ErrRoleRequired
	}
	if strings.TrimSpace(req.Scope) == "" {
		return ErrInvalidSubSpawn
	}
	return ctx.Err()
}

// subSpawnMetadata returns the metadata attached to every sub-chat request.
func subSpawnMetadata() map[string]string {
	return map[string]string{
		"tool":       "sub_spawn",
		"P2-PEWB-17": "preserved",
		"P2-ADVX-34": "preserved",
		"P2-HRNC-39": "preserved",
		"P2-LIFE-25": "preserved",
		"D4742":      "preserved",
		"D6044":      "preserved",
		"D6048":      "preserved",
		"D6099":      "preserved",
	}
}

// isValidSubChatID checks that id matches the format subchat:<positive decimal integer>.
func isValidSubChatID(id string) bool {
	if !strings.HasPrefix(id, "subchat:") {
		return false
	}
	numStr := id[len("subchat:"):]
	if numStr == "" {
		return false
	}
	n, err := strconv.Atoi(numStr)
	if err != nil {
		return false
	}
	return n > 0
}
