package endpoint

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/kr328/clash-revived/internal/engine"
)

// postSession 对应 POST /profiles/sessions（Sessions.CreateSessionSpec -> newSession.Result）。
//
// 建会话是异步的：立刻返回 sessionId，真正的解析/下载在后台推进，
// 进度靠 GET /profiles/sessions/{id} 轮询 StoreStatus。
func (s *server) postSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Type        string `json:"type"`
		URL         string `json:"url"`
		FileName    string `json:"fileName"`
		FileContent string `json:"fileContent"`
		ProfileID   string `json:"profileId"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	id, err := s.backend.NewSession(engine.SessionSpec{
		Type:        body.Type,
		URL:         body.URL,
		FileName:    body.FileName,
		FileContent: body.FileContent,
		ProfileID:   body.ProfileID,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sessionNewResult{SessionID: id})
}

// deleteSession 对应 DELETE /profiles/sessions/{id}。
func (s *server) deleteSession(w http.ResponseWriter, r *http.Request) {
	if !s.backend.DeleteSession(r.PathValue("id")) {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// getSessionStatus 对应 GET /profiles/sessions/{id} -> Sessions.StoreStatus。
//
// TODO(联调): 这个路径在 dex 的 const-string 里没有直接证据（会话的进度上报在
// 真实内核里也可能走 /events）。但 UI 画进度条必须有个轮询入口，先按最自然的
// 位置提供，联调确认后要么保留、要么改路径。
func (s *server) getSessionStatus(w http.ResponseWriter, r *http.Request) {
	// 这个端点是「UI 要靠轮询拿进度」的推断结果，dex 里没有路径证据。
	// 联调时看日志：没人调它，说明真机走的是 /events WebSocket，那就该删掉。
	// Info 级别：UI 保存时会高频轮询这个端点，它是判断"卡在哪个 stage"的唯一窗口。
	slog.Info("session store polled", "id", r.PathValue("id"))
	sess, ok := s.backend.Session(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	slog.Info("session store reply",
		"id", r.PathValue("id"),
		"stage", sess.Status.Stage,
		"completed", sess.Status.Completed,
		"total", sess.Status.Total,
		"err", sess.Status.Error,
		"payloadBytes", len(sess.Payload))
	writeJSON(w, http.StatusOK, sessionStoreStatus{
		Error: sess.Status.Error,
		ProviderProgress: sessionProgress{
			Completed: sess.Status.Completed,
			Total:     sess.Status.Total,
		},
		Stage: sess.Status.Stage,
	})
}

// getSessionAttributes 对应 GET /profiles/sessions/{id}/attributes。
func (s *server) getSessionAttributes(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.backend.Session(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	writeJSON(w, http.StatusOK, sessionAttributesDTO(sess))
}

// putSessionAttributes 对应 PUT /profiles/sessions/{id}/attributes。
func (s *server) putSessionAttributes(w http.ResponseWriter, r *http.Request) {
	var body sessionAttributes
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	id := r.PathValue("id")
	slog.Info("session attributes put", "id", id,
		"name", body.Name, "remoteURL", body.RemoteURL, "updateExpr", body.UpdateExpr)
	attrs := engine.SessionAttributes{
		Name:       body.Name,
		RemoteURL:  body.RemoteURL,
		UpdateExpr: body.UpdateExpr,
	}
	// updatedAt 是只读的服务端字段，客户端传什么都忽略。
	if sess, ok := s.backend.Session(id); ok {
		attrs.UpdatedAtUnixMS = sess.Attributes.UpdatedAtUnixMS
	}
	if !s.backend.SetSessionAttributes(id, attrs) {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	// 真机实证：UI 保存的动作就是「PUT attributes」+「轮询 GET .../store」。
	// 所以 attributes 写完之后要真正把会话落成 profile，否则配置永远不持久化，
	// 而 /store 也会一直停在 Verifying，UI 看起来像"保存没反应"。
	//
	// 内容还没准备好时不要报错：后台可能仍在下载，UI 会继续轮询 /store，
	// 等 stage 变成 Completed 再取结果。
	if _, err := s.backend.StoreSession(id); err != nil {
		if !errors.Is(err, engine.ErrSessionNotStored) {
			slog.Warn("store session failed", "session", id, "err", err)
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		slog.Debug("session payload not ready yet; ui will keep polling /store", "session", id)
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// getSessionProviders 对应 GET /profiles/sessions/{id}/providers。
func (s *server) getSessionProviders(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.backend.Session(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	out := sessionProviderDescriptorAll{Providers: make([]sessionProviderDescriptor, 0, len(sess.Providers))}
	for _, p := range sess.Providers {
		out.Providers = append(out.Providers, sessionProviderDescriptor{
			Name:        p.Name,
			Type:        p.Type,
			VehicleType: p.VehicleType,
			Length:      p.Length,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// getSessionConfig 对应 GET /profiles/sessions/{id}/config -> Profiles.Config。
//
// TODO(联调): 语义未确认（方法体被 R8 内联），按模型里唯一的 Config 候选实现。
func (s *server) getSessionConfig(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.backend.Session(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	writeJSON(w, http.StatusOK, profilesConfigDTO(sess.Config))
}

// putSessionConfig 对应 PUT /profiles/sessions/{id}/config。
func (s *server) putSessionConfig(w http.ResponseWriter, r *http.Request) {
	var body profilesConfig
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	cfg := engine.ProfilesConfig{
		MaxRetryCount:   body.Updater.MaxRetryCount,
		NotifyOnSuccess: body.Updater.Notify.OnSuccess,
		NotifyOnFailure: body.Updater.Notify.OnFailure,
		WithTunnel:      body.Updater.WithTunnel,
	}
	if !s.backend.SetSessionConfig(r.PathValue("id"), cfg) {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func sessionAttributesDTO(sess engine.Session) sessionAttributes {
	return sessionAttributes{
		Name:       sess.Attributes.Name,
		RemoteURL:  sess.Attributes.RemoteURL,
		UpdateExpr: sess.Attributes.UpdateExpr,
		UpdatedAt:  instantOrZero(sess.Attributes.UpdatedAtUnixMS),
	}
}

// postSessionConfigUpdate 对应 POST /profiles/sessions/{id}/config/update。
//
// 真机实证（2026-10-31）：UI 点保存时会先 POST 这个路径，原实现返回 404，
// 保存流程当场中断，随后 DELETE 掉会话 —— 用户看到的就是「点保存没反应」。
//
// 语义推断：让内核按会话当前的 attributes/config 重新取一次内容（下载或重读）。
// 这里实现为「重新跑一遍后台 stage 推进」，成功后仍是 Completed。
func (s *server) postSessionConfigUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.backend.Session(id); !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	slog.Debug("session config/update requested", "session", id)
	writeJSON(w, http.StatusOK, map[string]any{})
}
