package agent

import (
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/schema"
)

// RunOption configures Master.Run behavior.
type RunOption func(*runOptions)

type runOptions struct {
	verbose   io.Writer
	callbacks []callbacks.Handler
}

// WithVerbose prints every agent interaction event to w (typically os.Stderr).
func WithVerbose(w io.Writer) RunOption {
	return func(o *runOptions) {
		o.verbose = w
	}
}

// WithCallbacks attaches Eino callback handlers to Runner.Query (ChatModel/Tool etc.).
func WithCallbacks(handlers ...callbacks.Handler) RunOption {
	return func(o *runOptions) {
		o.callbacks = append(o.callbacks, handlers...)
	}
}

// WithSlog logs ChatModel and Tool lifecycle via slog (coexists with WithVerbose).
func WithSlog(logger *slog.Logger) RunOption {
	return WithCallbacks(NewSlogCallbackHandler(logger))
}

func applyRunOptions(opts []RunOption) runOptions {
	var out runOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&out)
		}
	}
	return out
}

func formatRunPath(steps []adk.RunStep) string {
	if len(steps) == 0 {
		return ""
	}
	parts := make([]string, 0, len(steps))
	for _, step := range steps {
		name := step.String()
		if name == "" {
			continue
		}
		parts = append(parts, name)
	}
	return strings.Join(parts, "/")
}

func logAgentEvent(w io.Writer, step int, event *adk.AgentEvent) {
	if w == nil || event == nil {
		return
	}
	path := formatRunPath(event.RunPath)
	agentName := event.AgentName
	if agentName == "" {
		agentName = "agent"
	}
	prefix := fmt.Sprintf("[%03d] %s", step, agentName)
	if path != "" {
		prefix = fmt.Sprintf("[%03d] %s (%s)", step, agentName, path)
	}

	if event.Err != nil {
		fmt.Fprintf(w, "%s ERROR: %v\n", prefix, event.Err)
		return
	}

	if event.Action != nil {
		switch {
		case event.Action.Exit:
			fmt.Fprintf(w, "%s ACTION: exit\n", prefix)
		case event.Action.TransferToAgent != nil:
			fmt.Fprintf(w, "%s ACTION: transfer -> %s\n", prefix, event.Action.TransferToAgent.DestAgentName)
		case event.Action.Interrupted != nil:
			fmt.Fprintf(w, "%s ACTION: interrupted\n", prefix)
		case event.Action.BreakLoop != nil:
			fmt.Fprintf(w, "%s ACTION: break_loop\n", prefix)
		default:
			fmt.Fprintf(w, "%s ACTION: %+v\n", prefix, event.Action)
		}
	}

	if event.Output == nil || event.Output.MessageOutput == nil {
		return
	}
	mv := event.Output.MessageOutput
	msg, err := mv.GetMessage()
	if err != nil {
		fmt.Fprintf(w, "%s MESSAGE_ERROR role=%s: %v\n", prefix, mv.Role, err)
		return
	}
	if msg == nil {
		return
	}

	role := string(mv.Role)
	if role == "" {
		role = string(msg.Role)
	}
	switch schema.RoleType(role) {
	case schema.Assistant:
		fmt.Fprintf(w, "%s ASSISTANT:\n", prefix)
		if msg.Content != "" {
			fmt.Fprintf(w, "%s\n", indentBlock(msg.Content))
		}
		for _, call := range msg.ToolCalls {
			fmt.Fprintf(w, "  tool_call id=%s name=%s\n", call.ID, call.Function.Name)
			if call.Function.Arguments != "" {
				fmt.Fprintf(w, "%s\n", indentBlock(call.Function.Arguments))
			}
		}
	case schema.Tool:
		name := mv.ToolName
		if name == "" {
			name = msg.ToolName
		}
		fmt.Fprintf(w, "%s TOOL_RESULT name=%s:\n", prefix, name)
		if msg.Content != "" {
			fmt.Fprintf(w, "%s\n", indentBlock(msg.Content))
		}
	case schema.User:
		fmt.Fprintf(w, "%s USER:\n%s\n", prefix, indentBlock(msg.Content))
	case schema.System:
		fmt.Fprintf(w, "%s SYSTEM:\n%s\n", prefix, indentBlock(msg.Content))
	default:
		if msg.Content != "" || len(msg.ToolCalls) > 0 {
			fmt.Fprintf(w, "%s MESSAGE role=%s content=%q tool_calls=%d\n",
				prefix, role, truncate(msg.Content, 200), len(msg.ToolCalls))
		}
	}
}

func indentBlock(s string) string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return "  (empty)"
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = "  " + line
	}
	return strings.Join(lines, "\n")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
