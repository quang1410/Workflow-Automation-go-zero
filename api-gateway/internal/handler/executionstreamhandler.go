package handler

import (
	"fmt"
	"net/http"
	"strings"

	"api-gateway/internal/svc"
	"api-gateway/internal/types"
	"api-gateway/model"

	"github.com/zeromicro/go-zero/rest/httpx"
)

// ExecutionStreamHandler streams real-time execution events via Server-Sent Events.
// It subscribes to the Redis pub/sub channel "executions:{workflowId}" and forwards
// each published event to the client until a "finished" event or client disconnect.
func ExecutionStreamHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.StreamExecutionReq
		if err := httpx.Parse(r, &req); err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}

		// Verify execution exists and get its workflow ID for the channel name.
		var ex model.Execution
		if err := svcCtx.DB.First(&ex, req.Id).Error; err != nil {
			httpx.ErrorCtx(r.Context(), w, fmt.Errorf("execution not found"))
			return
		}

		// If execution already finished, flush current status and close.
		if ex.Status == "success" || ex.Status == "failed" {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			fmt.Fprintf(w, "data: {\"type\":\"finished\",\"executionId\":%d,\"status\":\"%s\"}\n\n", ex.ID, ex.Status)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			return
		}

		// Execution is still running — subscribe to its channel.
		channel := fmt.Sprintf("executions:%d", ex.WorkflowID)
		pubsub := svcCtx.Redis.Subscribe(r.Context(), channel)
		defer pubsub.Close()

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		msgCh := pubsub.Channel()
		for {
			select {
			case msg, open := <-msgCh:
				if !open {
					return
				}
				fmt.Fprintf(w, "data: %s\n\n", msg.Payload)
				flusher.Flush()
				// Stop streaming once execution finishes.
				if isFinished(msg.Payload) {
					return
				}
			case <-r.Context().Done():
				return
			}
		}
	}
}

// isFinished returns true when the SSE payload contains a "finished" event.
func isFinished(payload string) bool {
	return strings.Contains(payload, `"type":"finished"`) ||
		strings.Contains(payload, `"type": "finished"`)
}
