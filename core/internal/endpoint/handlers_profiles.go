package endpoint

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/kr328/clash-revived/internal/engine"
)

// getProfiles 对应 GET /profiles -> Profiles.Profile.All。
//
// 只读实现（P1c 第一阶段）：以 mode=ro 打开 store.db，读 profiles + user_infos。
// 宿主没下发 store.db 路径时返回 503 而不是空列表——空列表会被误读成
// "用户还没有任何配置"，那比报错更难排查。
func (s *server) getProfiles(w http.ResponseWriter, r *http.Request) {
	list, err := s.backend.Profiles()
	if err != nil {
		if errors.Is(err, engine.ErrStorePathNotConfigured) {
			slog.Warn("GET /profiles: store path not configured")
			writeError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		slog.Error("GET /profiles failed", "err", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := profilesProfileAll{Profiles: make([]profilesProfile, 0, len(list))}
	for _, p := range list {
		out.Profiles = append(out.Profiles, profilesProfileDTO(p))
	}
	writeJSON(w, http.StatusOK, out)
}

// profilesConfigDTO 把 domain 配置转成线上模型（/profiles/config 与 session config 共用）。
func profilesConfigDTO(cfg engine.ProfilesConfig) profilesConfig {
	return profilesConfig{
		Updater: profilesUpdater{
			MaxRetryCount: cfg.MaxRetryCount,
			Notify: profilesUpdaterNotify{
				OnFailure: cfg.NotifyOnFailure,
				OnSuccess: cfg.NotifyOnSuccess,
			},
			WithTunnel: cfg.WithTunnel,
		},
	}
}

func profilesProfileDTO(p engine.Profile) profilesProfile {
	return profilesProfile{
		ID: p.ID,
		Attributes: profilesProfileAttributes{
			Name:       p.Name,
			RemoteURL:  p.RemoteURL,
			UpdateExpr: p.UpdateExpr,
			UpdatedAt:  instantOrZero(p.UpdatedAtUnixMS),
		},
		UserInfo: profilesProfileUserInfo{
			Upload:   p.Upload,
			Download: p.Download,
			Total:    p.Total,
			ExpireAt: instantOrZero(p.ExpireAtUnixMS),
		},
	}
}

// deleteProfile 对应 DELETE /profiles/{id}。
func (s *server) deleteProfile(w http.ResponseWriter, r *http.Request) {
	if err := s.backend.DeleteProfile(r.PathValue("id")); err != nil {
		s.writeProfileError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// putProfile 对应 PUT /profiles/{id}：**触发重新下载**（不是写客户端上传的内容）。
//
// 证据：`Profiles.update(UUID)` → PUT '/profiles/' + id，响应 Unit，
// 且对应的 $inlined lambda 没有 $body 字段。
func (s *server) putProfile(w http.ResponseWriter, r *http.Request) {
	if err := s.backend.RefreshProfile(r.PathValue("id")); err != nil {
		s.writeProfileError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// writeProfileError 把 engine 的错误映射成 HTTP 状态码。
func (s *server) writeProfileError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, engine.ErrProfileNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, engine.ErrProfileNoRemoteURL):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, engine.ErrStorePathNotConfigured):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	default:
		slog.Error("profile operation failed", "err", err)
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

// getProfilesConfig 对应 GET /profiles/config -> Profiles.Config。
//
// 读不到时返回**完整默认对象**（不是 {}、不是 500）：Profiles.Config 的字段必填，
// {} 会让 kotlinx 抛 MissingFieldException；而设置页不该因为库没接好就打不开。
func (s *server) getProfilesConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.backend.LoadProfilesConfig()
	if err != nil {
		slog.Warn("GET /profiles/config: fallback to defaults", "err", err)
		cfg = engine.ProfilesConfig{}
	}
	writeJSON(w, http.StatusOK, profilesConfigDTO(cfg))
}

// putProfilesConfig 对应 PUT /profiles/config。
func (s *server) putProfilesConfig(w http.ResponseWriter, r *http.Request) {
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
	if err := s.backend.SaveProfilesConfig(cfg); err != nil {
		// 写不进去必须让用户知道（否则他以为设置保存了）。
		if errors.Is(err, engine.ErrStorePathNotConfigured) {
			writeError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		slog.Error("PUT /profiles/config failed", "err", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// postProfileConfigUpdate 对应 POST /profiles/{id}/config/update。
//
// 与 session 版语义一致：触发该 profile 重新取内容。
// 复用 RefreshProfile（它会重新下载 remoteURL 并写回 content/version/updatedAt）。
func (s *server) postProfileConfigUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.backend.RefreshProfile(id); err != nil {
		switch {
		case errors.Is(err, engine.ErrProfileNotFound):
			writeError(w, http.StatusNotFound, err.Error())
		case errors.Is(err, engine.ErrProfileNoRemoteURL):
			writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, engine.ErrStorePathNotConfigured):
			writeError(w, http.StatusServiceUnavailable, err.Error())
		default:
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}
