package plugins

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	pacmd "github.com/acamarata/cascade/plugins/cascade-pa/cmd"
)

// The adapter records user input, dispatches generation, and reads the durable
// reply. Paths resolve at invocation time; importing the plugin does no I/O.
const cascadePAClientTimeout = 5 * time.Second
const chatReplyTimeout = 10 * time.Minute

func init() {
	pacmd.SetClient(newCascadePAClient(client.UnixDialer, cascadePAClientTimeout, runtime.NewDefaultPathProvider))
}

type pathResolver func() (runtime.PathProvider, error)

// rpcDoer permits transport-free tests of the client-side wire contract.
type rpcDoer interface {
	Do(ctx context.Context, method string, params, out any) error
}

type replySubscriber interface {
	StreamTopic(context.Context, string, url.Values) (<-chan client.Event, func(), error)
}

type cascadePAClient struct {
	dial         client.DialFunc
	timeout      time.Duration
	resolvePaths pathResolver
	doer         rpcDoer
	getwd        func() (string, error)
}

func newCascadePAClient(dial client.DialFunc, timeout time.Duration, resolvePaths pathResolver) *cascadePAClient {
	return &cascadePAClient{dial: dial, timeout: timeout, resolvePaths: resolvePaths, getwd: os.Getwd}
}

// These shapes mirror the private chat.append_turn server wire types.
type appendTurnParams struct {
	ThreadID       string              `json:"thread_id"`
	Role           string              `json:"role"`
	Segments       []appendSegmentWire `json:"segments"`
	PrivacyMode    string              `json:"privacy_mode,omitempty"`
	Origin         string              `json:"origin,omitempty"`
	Untrusted      bool                `json:"untrusted,omitempty"`
	LeadingContext string              `json:"leading_context,omitempty"`
	Cwd            string              `json:"cwd,omitempty"`
}
type appendSegmentWire struct {
	Kind    string `json:"kind"`
	Content string `json:"content"`
}
type appendTurnResult struct {
	ThreadID string `json:"thread_id"`
	TurnID   string `json:"turn_id"`
	Seq      int64  `json:"seq"`
}

func (c *cascadePAClient) rpcClient() (rpcDoer, error) {
	if c.doer != nil {
		return c.doer, nil
	}
	paths, err := c.resolvePaths()
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "cascade chat: resolve daemon socket path")
	}
	return client.New(paths.SocketPath(), c.dial, c.timeout), nil
}

// openReply records the user turn before requesting a server-bound stream ID.
func (c *cascadePAClient) openReply(ctx context.Context, rpc rpcDoer, req pacmd.OneShotRequest) (string, error) {
	params := appendTurnParams{ThreadID: req.Thread, Role: "user",
		Segments:    []appendSegmentWire{{Kind: "text", Content: req.Prompt}},
		PrivacyMode: req.Privacy, LeadingContext: req.LeadingContext, Cwd: c.cwd()}
	var turn appendTurnResult
	if err := rpc.Do(ctx, conversation.MethodAppendTurn, params, &turn); err != nil {
		return "", err
	}
	var opened struct {
		StreamID string `json:"stream_id"`
	}
	paramsOpen := struct {
		ThreadID string `json:"thread_id"`
		TurnID   string `json:"turn_id"`
	}{turn.ThreadID, turn.TurnID}
	if err := rpc.Do(ctx, "chat.reply_open", paramsOpen, &opened); err != nil {
		return "", err
	}
	return opened.StreamID, nil
}

// OneShot returns only the persisted assistant turn from a completed reply.
func (c *cascadePAClient) OneShot(ctx context.Context, req pacmd.OneShotRequest) (pacmd.OneShotResult, error) {
	ctx, cancel := context.WithTimeout(ctx, chatReplyTimeout)
	defer cancel()
	replyClient := *c
	replyClient.timeout = chatReplyTimeout
	rpc, err := replyClient.rpcClient()
	if err != nil {
		return pacmd.OneShotResult{}, err
	}
	id, err := c.openReply(ctx, rpc, req)
	if err != nil {
		return pacmd.OneShotResult{}, err
	}
	var reply conversation.ReplyResult
	if err := rpc.Do(ctx, "chat.reply", map[string]string{"stream_id": id}, &reply); err != nil {
		return pacmd.OneShotResult{}, err
	}
	return readReply(ctx, rpc, reply)
}

func readReply(ctx context.Context, rpc rpcDoer, reply conversation.ReplyResult) (pacmd.OneShotResult, error) {
	if err := ctx.Err(); err != nil {
		return pacmd.OneShotResult{}, err
	}
	var history getThreadResultWire
	if err := rpc.Do(ctx, conversation.MethodGetThread, getThreadParamsWire{ThreadID: reply.ThreadID}, &history); err != nil {
		return pacmd.OneShotResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return pacmd.OneShotResult{}, err
	}
	for _, turn := range history.Turns {
		if turn.Turn.ID != reply.AssistantTurnID || turn.Turn.Role != "assistant" {
			continue
		}
		var text strings.Builder
		for _, segment := range turn.Segments {
			if segment.Kind == "text" {
				text.WriteString(segment.Content)
			}
		}
		return pacmd.OneShotResult{ThreadID: reply.ThreadID, TurnID: reply.AssistantTurnID, Content: text.String()}, nil
	}
	return pacmd.OneShotResult{}, cascade.New(cascade.KindIntegrity, "cascade chat: completed reply has no durable assistant turn")
}

// Stream subscribes before dispatch, emits deltas, and verifies the durable reply.
func (c *cascadePAClient) Stream(ctx context.Context, req pacmd.OneShotRequest) (<-chan string, <-chan error) {
	tokens, errs := make(chan string), make(chan error, 1)
	go func() {
		err := c.streamReply(ctx, req, tokens)
		close(tokens)
		errs <- err
		close(errs)
	}()
	return tokens, errs
}

func (c *cascadePAClient) streamReply(ctx context.Context, req pacmd.OneShotRequest, tokens chan<- string) error {
	ctx, cancel := context.WithTimeout(ctx, chatReplyTimeout)
	defer cancel()
	replyClient := *c
	replyClient.timeout = chatReplyTimeout
	rpc, err := replyClient.rpcClient()
	if err != nil {
		return err
	}
	id, err := c.openReply(ctx, rpc, req)
	if err != nil {
		return err
	}
	subscriber, ok := rpc.(replySubscriber)
	if !ok {
		return cascade.New(cascade.KindInternal, "cascade chat: reply subscription unavailable")
	}
	frames, closeStream, err := subscriber.StreamTopic(ctx, "chat.reply", url.Values{"stream_id": {id}})
	if err != nil {
		return err
	}
	defer closeStream()
	completed := make(chan error, 1)
	var reply conversation.ReplyResult
	go func() {
		defer close(completed)
		completed <- rpc.Do(ctx, "chat.reply", map[string]string{"stream_id": id}, &reply)
	}()
	if err := forwardReply(ctx, frames, tokens, completed); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-completed:
		if err != nil {
			return err
		}
	}
	_, err = readReply(ctx, rpc, reply)
	return err
}

func forwardReply(ctx context.Context, frames <-chan client.Event, tokens chan<- string, completed <-chan error) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-completed:
			if err != nil {
				return err
			}
			completed = nil
		case event, ok := <-frames:
			if !ok {
				return cascade.New(cascade.KindUnavailable, "cascade chat: reply stream ended before terminal frame")
			}
			var frame conversation.Frame
			if err := json.Unmarshal([]byte(event.Data), &frame); err != nil {
				return cascade.Wrap(cascade.KindIntegrity, err, "cascade chat: invalid reply frame")
			}
			switch frame.Type {
			case "delta":
				select {
				case tokens <- frame.Delta:
				case <-ctx.Done():
					return ctx.Err()
				}
			case "done":
				return nil
			case "error":
				return cascade.New(cascade.KindUnavailable, "cascade chat: reply failed: "+frame.Kind)
			default:
				return cascade.New(cascade.KindIntegrity, "cascade chat: unknown reply frame")
			}
		}
	}
}

type resumeParams struct {
	TopicID         string `json:"topic_id,omitempty"`
	Query           string `json:"query,omitempty"`
	SessionThreadID string `json:"session_thread_id,omitempty"`
	Cwd             string `json:"cwd,omitempty"`
}
type resumeResult struct {
	ThreadID       string   `json:"thread_id"`
	LeadingContext string   `json:"leading_context"`
	Tokens         int      `json:"tokens"`
	Trimmed        []string `json:"trimmed"`
}

// Resume composes the daemon's recalled context into the current session.
func (c *cascadePAClient) Resume(ctx context.Context, req pacmd.ResumeRequest) (pacmd.ResumeResult, error) {
	rpc, err := c.rpcClient()
	if err != nil {
		return pacmd.ResumeResult{}, err
	}
	params := resumeParams{TopicID: req.TopicID, Query: req.Query, SessionThreadID: req.Thread, Cwd: req.Cwd}
	var result resumeResult
	if err := rpc.Do(ctx, conversation.MethodResume, params, &result); err != nil {
		return pacmd.ResumeResult{}, err
	}
	return pacmd.ResumeResult{ThreadID: result.ThreadID, LeadingContext: result.LeadingContext, Tokens: result.Tokens, Trimmed: result.Trimmed}, nil
}

type createThreadParams struct {
	PrivacyMode string `json:"privacy_mode"`
	Cwd         string `json:"cwd,omitempty"`
}
type createThreadResult struct {
	ThreadID string `json:"thread_id"`
}

// CreateThread asks the daemon to mint a thread with its privacy marker.
func (c *cascadePAClient) CreateThread(ctx context.Context, privacyMode string) (string, error) {
	rpc, err := c.rpcClient()
	if err != nil {
		return "", err
	}
	var result createThreadResult
	if err := rpc.Do(ctx, conversation.MethodCreateThread, createThreadParams{PrivacyMode: privacyMode, Cwd: c.cwd()}, &result); err != nil {
		return "", err
	}
	return result.ThreadID, nil
}

func (c *cascadePAClient) cwd() string {
	if c.getwd == nil {
		return ""
	}
	dir, err := c.getwd()
	if err != nil {
		return ""
	}
	return dir
}
