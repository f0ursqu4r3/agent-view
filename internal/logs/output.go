package logs

import "agent-view/internal/wire"

// Limits shared by the log adapters.
const (
	MaxPending      = 5000
	MaxCommandChars = 4096
	MaxChunkBytes   = 4096
	MaxOutputBytes  = 16 * 1024
)

// chunkBytes splits UTF-8 bytes into chunks of at most size bytes without
// cutting a character.
func chunkBytes(buf []byte, size int) []string {
	var chunks []string
	start := 0
	for start < len(buf) {
		end := min(start+size, len(buf))
		if end < len(buf) {
			for end > start && isContinuation(buf[end]) {
				end--
			}
		}
		if end == start {
			end = min(start+size, len(buf))
		}
		chunks = append(chunks, validUTF8(string(buf[start:end])))
		start = end
	}
	return chunks
}

// tailBytes returns the last limit bytes of buf, starting on a character
// boundary, and whether it cut anything.
func tailBytes(buf []byte, limit int) ([]byte, bool) {
	if len(buf) <= limit {
		return buf, false
	}
	start := len(buf) - limit
	for start < len(buf) && isContinuation(buf[start]) {
		start++
	}
	return buf[start:], true
}

// OutputEvents returns the tool.output events for one command result: the
// last 16 KiB in chunks of at most 4 KiB. The first chunk carries Truncated
// when the output was cut. Empty output gives no event.
func OutputEvents(ts, agentID, callID, text string) []wire.SourceEvent {
	buf, cut := tailBytes([]byte(validUTF8(text)), MaxOutputBytes)
	chunks := chunkBytes(buf, MaxChunkBytes)
	if len(chunks) == 0 {
		return nil
	}
	events := make([]wire.SourceEvent, len(chunks))
	for i, chunk := range chunks {
		events[i] = wire.SourceEvent{Ts: ts, Ev: wire.ToolOutput{
			AgentID: agentID, CallID: callID, Chunk: chunk, Truncated: cut && i == 0,
		}}
	}
	return events
}
