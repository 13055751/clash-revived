package endpoint

import (
	"errors"
	"net/http"

	"github.com/kr328/clash-revived/internal/engine"
)

// getExecutorConfig 对应 GET /executor/config -> Executor.Config。
func (s *server) getExecutorConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.backend.ExecutorConfig()
	writeJSON(w, http.StatusOK, executorConfigDTO(cfg))
}

// patchExecutorConfig 对应 PATCH /executor/config。
//
// UI 切换订阅只发这一条请求（`ViewModel$select$1`），所以这里**必须真的重载 tunnel**，
// 不能只记 activeProfile——否则用户点一下订阅列表什么都不会发生。
//
// 部分更新语义：body 里没出现的字段保持原值（Kotlin 侧只带 activeProfile 时，
// 不应把 externalController 清空）。
func (s *server) patchExecutorConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ActiveProfile            *string `json:"activeProfile"`
		ExternalController       *string `json:"externalController"`
		ExternalControllerSecret *string `json:"externalControllerSecret"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if body.ActiveProfile == nil {
		// 只带 externalController 的 patch 等于空操作，明确拒绝而不是假装成功。
		writeError(w, http.StatusBadRequest, "nothing to patch: externalController is managed by /listeners/ExternalController")
		return
	}

	// externalController / externalControllerSecret **故意忽略**：
	// 真正的监听控制属于 /listeners/ExternalController/*（Listeners.Listener.Config
	// 里有 listenAt + secret 的对象形态）。这里再存一份就会变成"两个真相源"：
	// UI 显示 controller 在 127.0.0.1:9090，实际根本没监听。
	// 所以 PATCH 只处理 activeProfile；只带 controller 字段的请求视为"什么都没做"。
	// TODO(P2): /listeners/ExternalController 的开关与配置实现后，由那边统一管。

	if body.ActiveProfile != nil {
		if err := s.backend.SetActiveProfile(*body.ActiveProfile); err != nil {
			// 失败必须可见：Kotlin 的 select 协程靠状态码判断。
			switch {
			case errors.Is(err, engine.ErrProfileNotFound), errors.Is(err, engine.ErrProfileNoContent):
				writeError(w, http.StatusNotFound, err.Error())
			case errors.Is(err, engine.ErrStorePathNotConfigured):
				writeError(w, http.StatusServiceUnavailable, err.Error())
			default:
				// 解析失败 / 启动失败：400，让 UI 显示错误而不是"切换成功"。
				writeError(w, http.StatusBadRequest, err.Error())
			}
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func executorConfigDTO(cfg engine.ExecutorConfig) executorConfig {
	return executorConfig{
		ActiveProfile:            cfg.ActiveProfile,
		ExternalController:       cfg.ExternalController,
		ExternalControllerSecret: cfg.ExternalControllerSecret,
	}
}
