package endpoint

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

// /traffic 与 /bandwidth 都是 WebSocket 长连接推送，帧类型是同一份
// `Transmit.Bytes`（证据：`Traffic$poll$1` 与 `Bandwidth$poll$1` 的 invokeSuspend
// 都取 `Transmit$Bytes$Companion->serializer()` 作为 rx 序列化器）。
//
// 两者的差别只在语义：
//   - /traffic   -> 瞬时速率（每秒的字节增量）
//   - /bandwidth -> 累计总量
//
// 推送节奏跟随 mihomo 自己的统计周期（statistic 的 blip 每秒刷新一次），
// 不自造 ticker 频率，避免 UI 图表观感与原版不一致。

// streamInterval 与 mihomo 统计周期一致。
const streamInterval = time.Second

func (s *server) getTraffic(w http.ResponseWriter, r *http.Request) {
	s.serveByteStream(w, r, func() transmitBytes {
		up, down := s.backend.Traffic()
		return transmitBytes{Up: up, Down: down}
	})
}

func (s *server) getBandwidth(w http.ResponseWriter, r *http.Request) {
	s.serveByteStream(w, r, func() transmitBytes {
		up, down := s.backend.Total()
		return transmitBytes{Up: up, Down: down}
	})
}

// serveByteStream 是两条流共用的推送循环。
func (s *server) serveByteStream(w http.ResponseWriter, r *http.Request, sample func() transmitBytes) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// UI 是原生 Ktor 客户端，不存在跨域问题；放开 Origin 校验以免真机上被拒。
		InsecureSkipVerify: true,
	})
	if err != nil {
		// 握手失败（比如客户端用普通 GET 探测）不是错误，Debug 级别即可。
		slog.Debug("websocket accept failed", "path", r.URL.Path, "err", err)
		return
	}
	defer func() { _ = conn.CloseNow() }()

	ctx := r.Context()
	ticker := time.NewTicker(streamInterval)
	defer ticker.Stop()

	// 必须有读循环：客户端主动关闭时要立刻感知，否则 Close 握手要等到超时
	// （实测会拖 5 秒），日志和资源都会被拖住。这条流是单向推送，收到的数据直接丢弃。
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				return
			}
		}
	}()

	// 先发一帧：UI 打开图表时不该等满一个周期才看到第一个点。
	if !writeStreamFrame(ctx, conn, sample()) {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-closed:
			slog.Debug("stream client closed", "path", r.URL.Path)
			return
		case <-ticker.C:
			if !writeStreamFrame(ctx, conn, sample()) {
				return
			}
		}
	}
}

// writeStreamFrame 写一帧；返回 false 表示流该结束了。
func writeStreamFrame(ctx context.Context, conn *websocket.Conn, payload transmitBytes) bool {
	body, err := json.Marshal(payload)
	if err != nil {
		slog.Error("marshal stream frame failed", "err", err)
		return false
	}
	// 写超时防止客户端卡死把内核拖住。
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := conn.Write(writeCtx, websocket.MessageText, body); err != nil {
		// 客户端主动断开是常态（切页面就会断开），不能刷 ERROR 日志。
		slog.Debug("stream closed", "err", err)
		return false
	}
	return true
}
