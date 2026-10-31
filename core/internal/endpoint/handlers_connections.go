package endpoint

import "net/http"

// getConnectionsCount 对应 GET /connections/count -> Connections.Count{now, history}。
//
// now = 当前活跃连接数；history = 引擎启动以来出现过的连接总数。
// mihomo 只维护活跃连接表，历史数由 engine.ConnectionCount() 惰性统计（见 stats.go）。
func (s *server) getConnectionsCount(w http.ResponseWriter, r *http.Request) {
	now, history := s.backend.ConnectionCount()
	writeJSON(w, http.StatusOK, connectionsCount{History: history, Now: now})
}
