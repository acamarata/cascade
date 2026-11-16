// Package background provides sub-spawn tooling.
package background

import (
	"context"
	"errors"
	"strings"
)

// ErrRoleRequired is returned when the Role field is empty or whitespace.
var ErrRoleRequired = errors.New("role is required")

// ErrInvalidSubSpawn is returned when a SubSpawnRequest fails validation.
var ErrInvalidSubSpawn = errors.New("invalid sub_spawn request")

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

// SubSpawn validates the inputs and dispatches to the appropriate tool.
func SubSpawn(ctx context.Context, chats SubChats, registry SubSpawnRegistry, req SubSpawnRequest) (SubSpawnReport, error) {
	if ctx == nil {
		return SubSpawnReport{}, ErrInvalidSubSpawn
	}
	if chats == nil {
		return SubSpawnReport{}, ErrInvalidSubSpawn
	}
	if registry == nil {
		return SubSpawnReport{}, ErrInvalidSubSpawn
	}
	if strings.TrimSpace(req.Task) == "" {
		return SubSpawnReport{}, ErrInvalidSubSpawn
	}
	if strings.TrimSpace(req.Role) == "" {
		return SubSpawnReport{}, ErrRoleRequired
	}
	if strings.TrimSpace(req.Scope) == "" {
		return SubSpawnReport{}, ErrInvalidSubSpawn
	}

	tool, ok := registry[req.Task]
	if !ok {
		return SubSpawnReport{}, ErrInvalidSubSpawn
	}
	return tool(ctx, req)
}
