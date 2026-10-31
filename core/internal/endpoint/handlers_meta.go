package endpoint

import (
	"net/http"

	"github.com/kr328/clash-revived/internal/engine"
)

// getStatus 对应 GET /status -> Executor.Status。
func (s *server) getStatus(w http.ResponseWriter, r *http.Request) {
	st := executorStatus{
		ProfileID: s.backend.ProfileID(),
		Type:      s.backend.State(),
	}
	if msg := s.backend.LastError(); msg != "" {
		st.Error = &msg
	}
	writeJSON(w, http.StatusOK, st)
}

// getStage 对应 GET /stage -> app/core 的 CoreManager.Stage（sealed）。
//
// 路径来自 API_CONTRACT 的「推断」条目，dex 里 /stage 只出现在 UriID 的字符串表中，
// 因此判别符与映射关系都标 TODO(联调)。
func (s *server) getStage(w http.ResponseWriter, r *http.Request) {
	out := stageDTO{Type: "Dead"}
	switch s.backend.State() {
	case engine.StatusLoading:
		out.Type = "Initializing"
	case engine.StatusLoaded:
		out.Type = "Running"
	case engine.StatusError:
		out.Type = "Failed"
		// CoreManager.Stage.Failed.Reason 只有 ElevateFailed / Unknown 两值；
		// 配置解析失败属于 Unknown（ElevateFailed 是 Shizuku/root 提权失败专用）。
		reason := "Unknown"
		out.Reason = &reason
	}
	writeJSON(w, http.StatusOK, out)
}

// getHealthcheck 对应 GET /healthcheck -> 存活探测。
// 返回体在契约里是「推断」，这里只给一个最小对象。
func (s *server) getHealthcheck(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// getConfig 对应 GET /config。
//
// TODO(P0 未决): dex 里没有能找到这个端点响应体类型的证据——
// 路径字面量不在任何 const-string 里，也没有对应的 @Serializable 数据类。
// 先返回空对象：如果 Kotlin 侧的模型有必填字段，kotlinx 抛出的异常会直接告诉
// 我们缺哪个字段名，这比瞎编一批字段更容易收敛。联调时用真机抓包补齐。
func (s *server) getConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{})
}

// getAttributes 对应 GET /attributes。
// TODO(P0 未决): 同 /config，响应体类型无二进制证据，先返回空对象。
func (s *server) getAttributes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{})
}
