#!/usr/bin/env python3
"""从 api_dataclasses.json 生成 Go 数据模型。

只生成真正参与 HTTP JSON 交换的类型；协程内部类、Ktor/OkHttp 运行时对象排除。
字段名保持 Kotlin 原样作为 JSON key —— kotlinx.serialization 默认以属性名为键。
"""
import json
import re
import sys

SRC = 'api_dataclasses.json'

# 序列化名覆盖：来自 classes.dex 里 annotated enum 的 EnumDescriptor 名字数组。
# 只有 General$Config.Mode 带名字数组（见 General$Config.<clinit> code@1574156：
#   const-string "direct"/"global"/"rule"/"script" + filled-new-array + EnumDescriptor）。
# 其余枚举均无名字数组，走 Enum.name()，即首字母大写。
SERIALIZED_OVERRIDE = {
    'General$Config$Mode': {'Direct': 'direct', 'Global': 'global',
                            'Rule': 'rule', 'Script': 'script'},
}
OUT = 'core-new/internal/model/model.go'

# 需要生成的 JSON 模型（白名单：仅 HTTP 请求/响应载荷）
INCLUDE = [
    # executor
    'Executor$Config', 'Executor$Status', 'Executor$Status$Type',
    # general
    'General$Config', 'General$Config$Mode',
    # connections / traffic
    'Connections$Count', 'Transmit$Bytes',
    # listeners
    'Listeners$Listener', 'Listeners$Listener$All', 'Listeners$Type',
    'Listeners$Listener$Config$DNS',
    'Listeners$Listener$Config$ExternalController',
    'Listeners$Listener$Config$HTTPSocks5',
    'Listeners$Listener$Config$Redirect',
    'Listeners$Listener$Config$SystemProxy',
    'Listeners$Listener$Config$TUN',
    'Listeners$Listener$Config$TransProxy',
    'Listeners$Listener$Config$VPN',
    'Listeners$Listener$Config$VPN$AccessControlConfig',
    'Listeners$Listener$Config$VPN$AccessControlConfig$Mode',
    'Listeners$Listener$Config$VPN$RouteMode',
    'Listeners$Listener$Config$VPN$SystemProxyConfig',
    'Listeners$Listener$Status',
    'Listeners$Listener$Status$DNS',
    'Listeners$Listener$Status$ExternalController',
    'Listeners$Listener$Status$HTTPSocks5',
    'Listeners$Listener$Status$Redirect',
    'Listeners$Listener$Status$SystemProxy',
    'Listeners$Listener$Status$TUN',
    'Listeners$Listener$Status$TransProxy',
    'Listeners$Listener$Status$VPN',
    # network
    'Network$FakeIP', 'Network$Interface', 'Network$Outbound',
    'Network$Resolver', 'Network$Resolver$FallbackFilter',
    # profiles
    'Profiles$Config', 'Profiles$Config$Updater', 'Profiles$Config$Updater$Notify',
    'Profiles$Profile', 'Profiles$Profile$All',
    'Profiles$Profile$Attributes', 'Profiles$Profile$UserInfo',
    # providers / proxies
    'Providers$Provider', 'Providers$Provider$All', 'Providers$Type',
    'Proxies$Proxy', 'Proxies$Proxy$Delay', 'Proxies$Type',
    'RuleProviders',
    # sessions
    'Sessions$CreateSessionSpec', 'Sessions$CreateSessionSpec$Type',
    'Sessions$ProviderDescriptor', 'Sessions$ProviderDescriptor$All',
    'Sessions$StoreStatus', 'Sessions$StoreStatus$CompletionProgress',
    'Sessions$StoreStatus$Stage', 'Sessions$Result',
    # events
    'Events$Event$Executor', 'Events$Event$Executor$Type',
    'Events$Event$Listener', 'Events$Event$Listener$Type',
    'Events$Event$Payload', 'Events$Event$Payload$Source',
    'Events$Event$Profile', 'Events$Event$Profile$Type',
    # websocket
    'WebSocket$Message$Bytes', 'WebSocket$Message$Text',
    # errors
    'RemoteError', 'ResponseException',
]

# 枚举（只有 Type/Mode/Stage/Source 这类，值未知，用 string 承载）
ENUM_HINT = re.compile(r'\$(Type|Mode|Stage|Source|RouteMode)$')

# 运行时对象 -> Go 类型
RUNTIME = {
    'String': 'string', 'UUID': 'string', 'Instant': 'string', 'HttpUrl': 'string',
    'Z': 'bool', 'Boolean': 'bool',
    'J': 'int64', 'I': 'int32',
    'List': '[]any', 'Map': 'map[string]any',
    'JsonElement': 'json.RawMessage',
    '[B': '[]byte',
    'Object': 'any',
}


def go_type(t, known, enums):
    if t in enums:
        return go_name(t)
    if t in RUNTIME:
        return RUNTIME[t]
    # 数组形式
    if t.startswith('['):
        inner = t[1:]
        return '[]' + go_type(inner, known, enums)
    if t in known:
        return 'GoName(t)'
    return 'any'


def go_name(k):
    return k.replace('$', '')


def go_field(name):
    if not name:
        return 'X'
    s = name[0].upper() + name[1:]
    return s


def json_tag(name):
    return name


def load_enums():
    try:
        return json.load(open('api_enums.json', encoding='utf-8'))
    except OSError:
        return {}


def main():
    d = json.load(open(SRC, encoding='utf-8'))
    enums = load_enums()
    known = {k for k in INCLUDE if k in d}
    missing = [k for k in INCLUDE if k not in d]
    if missing:
        print("# 白名单里但 json 中不存在的:", missing, file=sys.stderr)

    lines = []
    lines.append('// Code generated from api_dataclasses.json by gen_model.py. DO NOT EDIT MANUALLY.')
    lines.append('//')
    lines.append('// 这些结构体是 Kotlin 侧 kotlinx.serialization 的镜像。')
    lines.append('// 字段名即 JSON key（kotlinx 默认取属性名），改动会导致宿主反序列化失败。')
    lines.append('package model')
    lines.append('')
    lines.append('import "encoding/json"')
    lines.append('')

    # 枚举：类型 + 常量（取值来自 dex 的枚举字段名）
    lines.append('// --- 枚举 ---')
    lines.append('// 常量名 = Kotlin 枚举名；常量值 = 线上 JSON 字面量。')
    lines.append('// 默认两者相同（kotlinx 用 Enum.name()）。')
    lines.append('// 例外：General$Config$Mode 带 @SerialName 烘焙成的小写名数组，值因此不同。')
    lines.append('// 证据见 gen_model.py 顶部 SERIALIZED_OVERRIDE 的注释。')
    emitted = set()
    for k in sorted(enums):
        gn = go_name(k)
        if gn in emitted:
            continue
        emitted.add(gn)
        lines.append('')
        lines.append('type %s string' % gn)
        lines.append('')
        lines.append('const (')
        override = SERIALIZED_OVERRIDE.get(k, {})
        for v in enums[k]:
            wire = override.get(v, v)
            lines.append('\t%s%s %s = "%s"' % (gn, v, gn, wire))
        lines.append(')')
    lines.append('')

    lines.append('// --- 数据结构 ---')
    for k in sorted(known):
        if ENUM_HINT.search(k):
            continue
        gname = go_name(k)
        lines.append('')
        lines.append('// %s mirrors %s' % (gname, k))
        lines.append('type %s struct {' % gname)
        for f in d[k]:
            fname = f['name']
            ftype = f['type']
            gt = go_type(ftype, known, enums)
            if gt == 'GoName(t)':
                gt = go_name(ftype)
            lines.append('\t%s %s `json:"%s,omitempty"`' % (go_field(fname), gt, json_tag(fname)))
        lines.append('}')

    lines.append('')
    lines.append('// 确保 json 被使用（RawMessage 字段依赖）')
    lines.append('var _ = json.RawMessage(nil)')
    lines.append('')

    open(OUT, 'w', encoding='utf-8').write('\n'.join(lines))
    print("wrote %s: %d types (%d enums)" % (
        OUT, len(known), sum(1 for k in known if ENUM_HINT.search(k))))


if __name__ == '__main__':
    main()
