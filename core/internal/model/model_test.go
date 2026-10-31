package model

import (
	"encoding/json"
	"testing"
)

// 校验 JSON 键名与 kotlinx.serialization 的默认行为一致（键 = 属性名）。
func TestJSONKeys(t *testing.T) {
	c := ConnectionsCount{Now: 7, History: 42}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"history":42,"now":7}`
	if string(b) != want {
		t.Fatalf("got %s want %s", b, want)
	}
}

func TestExecutorConfig(t *testing.T) {
	e := ExecutorConfig{
		ActiveProfile:            "0f5f1d0a-0000-0000-0000-000000000000",
		ExternalController:       "127.0.0.1:9090",
		ExternalControllerSecret: "s3cr3t",
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"activeProfile", "externalController", "externalControllerSecret"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("missing key %q in %s", k, b)
		}
	}
}

// 枚举值必须与 Kotlin 侧枚举名逐字一致。
func TestEnumValues(t *testing.T) {
	cases := map[string]string{
		"Direct":          string(ProxiesTypeDirect),
		"Selector":        string(ProxiesTypeSelector),
		"rule":            string(GeneralConfigModeRule),
		"global":          string(GeneralConfigModeGlobal),
		"AllowAll":        string(ListenersListenerConfigVPNAccessControlConfigModeAllowAll),
		"ExcludePrivate?": "ExcludesPrivate",
	}
	for want, got := range cases {
		if want == "ExcludePrivate?" {
			continue
		}
		if want != got {
			t.Fatalf("enum mismatch: want %q got %q", want, got)
		}
	}
	// General.Config.Mode 是唯一带 @SerialName 名字数组的枚举，线上值为小写。
	if string(GeneralConfigModeDirect) != "direct" || string(GeneralConfigModeScript) != "script" {
		t.Fatalf("GeneralConfigMode wire value wrong: %q %q",
			GeneralConfigModeDirect, GeneralConfigModeScript)
	}
	if string(ListenersListenerConfigVPNRouteModeExcludesPrivate) != "ExcludesPrivate" {
		t.Fatalf("RouteMode wrong: %q", ListenersListenerConfigVPNRouteModeExcludesPrivate)
	}
}

// 空结构体不应产生 null 之外的意外键。
func TestOmitEmpty(t *testing.T) {
	b, err := json.Marshal(NetworkInterface{})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "{}" {
		t.Fatalf("empty struct marshalled to %s, want {}", b)
	}
}
