# 合约补充

1. `package provider`（`github.com/yetone/magpie/internal/provider`）的 Key 管理函数，签名与语义：
   - `UseKey(id, keyRef string) error`：把该 Key 设为"当前使用"（即 providers.json 里的顶层 `key`），原来的当前 Key 保持启用并排在其后；两者的名称与 `protocol` 限定都随 Key 保留。
   - `SetKeyOn(id, keyRef string, on bool) error`：启用/停用某个 Key（停用写作该 Key 的 `off: true`）。停用当前第一个 Key 时，下一个启用的 Key 接替为顶层 `key`，原第一个 Key 变为停用并保留；若没有其他启用的 Key，返回错误且不改动。
   - `RemoveKey(id, keyRef string) error`：删除 Key；最后一个启用的 Key 不可删除（返回错误）。删除当前第一个 Key 时下一个启用的接替。
   - `RenameKey(id, keyRef, name string) error`：修改 Key 的名称（当前 Key 的名称为顶层 `keyName`）。
   - 其中 `keyRef`（含 `SetKeyProtocol` 的 `keyRef`）是 Key 的指纹：对 Key 原文做 SHA-256，取前 5 个字节的十六进制小写字符串（10 个字符）。
2. `gateway.Call`（`Server.Recent()` 返回的调用记录）的 `Fallback string`（JSON `fallback`）：当请求在成功前有候选失败过（回退模型或多 Key 接力）时非空，内容包含失败候选的供应商 id；失败的候选是某个带名称的 Key 时还包含该 Key 的名称。`Call.Provider` 为最终完成请求的供应商 id。
