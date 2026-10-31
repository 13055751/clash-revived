package endpoint

import (
	"net/http"

	"github.com/kr328/clash-revived/internal/engine"
)

// getGeneral 对应 GET /general -> General.Config。
//
// 枚举线上是小写（唯一带 @SerialName 名字数组的枚举，见 model.GeneralConfigMode）。
func (s *server) getGeneral(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, generalConfig{Mode: modeToWire(s.backend.Mode())})
}

// patchGeneral 对应 PATCH /general。mode 切换是**真生效**的（mihomo 的 SetMode 很轻，
// 不需要重建 tunnel），所以这里不能像 /network 那样只记快照。
func (s *server) patchGeneral(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Mode *string `json:"mode"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if body.Mode == nil {
		writeError(w, http.StatusBadRequest, "nothing to patch: mode is the only field")
		return
	}
	mode, ok := modeFromWire(*body.Mode)
	if !ok {
		writeError(w, http.StatusBadRequest, "unknown mode: "+*body.Mode)
		return
	}
	if err := s.backend.SetMode(mode); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// modeToWire 把 engine 的规范名（Rule/Global/Direct/Script）转成线上小写。
func modeToWire(mode string) string {
	switch mode {
	case engine.ModeDirect:
		return "direct"
	case engine.ModeGlobal:
		return "global"
	case engine.ModeRule:
		return "rule"
	case engine.ModeScript:
		return "script"
	default:
		return "rule"
	}
}

// modeFromWire 反向转换。
func modeFromWire(wire string) (string, bool) {
	switch wire {
	case "direct", engine.ModeDirect:
		return engine.ModeDirect, true
	case "global", engine.ModeGlobal:
		return engine.ModeGlobal, true
	case "rule", engine.ModeRule:
		return engine.ModeRule, true
	case "script", engine.ModeScript:
		return engine.ModeScript, true
	default:
		return "", false
	}
}
